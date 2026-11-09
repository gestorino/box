package converse

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "concierge/engine/internal/gen/agent/v1"
	"concierge/engine/internal/gen/agent/v1/agentv1connect"
	"concierge/engine/internal/signins"
)

const (
	// MaxTools is how many tool calls one turn may make; the next one ends it with couldNotFinish.
	MaxTools = 8
	// SeenEvents is how many of the conversation's last events a step shows the agent.
	SeenEvents = 60

	couldNotFinish  = "I couldn't finish that, please try again"
	humanCheckWords = "The site wants a human check. Open the errand in the console"

	// linkWait is how long an errand's event waits for its run's link: start_errand links the run only
	// after StartErrand returns, and the errand may tell something first. linkPoll is how often it looks.
	linkWait = 5 * time.Second
	linkPoll = 100 * time.Millisecond

	// stepTimeout bounds one step: a daemon's own run is bounded (two CLI runs of 120 s at most), so
	// this only keeps a hung daemon from holding the conversation's queue for ever.
	stepTimeout     = 5 * time.Minute
	sendTimeout     = 30 * time.Second
	settingsTimeout = 10 * time.Second
	// retryFirst and retryMax bound the wait between reads of settings that could not be read: from
	// the first, doubling, up to the most, until a read succeeds or the hub stops.
	retryFirst = time.Second
	retryMax   = 30 * time.Second

	// noConversationCap bounds the runs remembered as no conversation's: past it they are forgotten.
	noConversationCap = 1024

	// restarted is the result of a tool call a stop cut short before its result was kept.
	restarted = `{"error":"the box restarted before this finished"}`
)

// labels are the agents' names as the owner reads them.
var labels = map[string]string{"claude": "Claude", "codex": "Codex"}

// reasons are the words for each step failure that falls back to the next agent.
var reasons = map[connect.Code]string{
	connect.CodeUnauthenticated:   "not signed in",
	connect.CodeResourceExhausted: "out of its plan's limit",
	connect.CodeUnavailable:       "not reachable",
}

// label is the agent's name as the owner reads it.
func label(name string) string {
	if l, ok := labels[name]; ok {
		return l
	}
	return name
}

// fallsBack is a step failure the next agent answers instead: not signed in, out of the plan's limit,
// or not reachable.
func fallsBack(err error) bool {
	_, ok := reasons[connect.CodeOf(err)]
	return ok
}

// switchNotice tells the owner that the agent from can't answer, for the reason of code, and to does.
func switchNotice(from, to string, code connect.Code) string {
	return label(from) + " can't answer now (" + reasons[code] + "); " + label(to) + " is answering."
}

// cantAnswer tells the owner that the agent can't answer, for the reason of code.
func cantAnswer(name string, code connect.Code) string {
	return label(name) + " can't answer now (" + reasons[code] + ")."
}

// waits are the kinds of an errand's event that wait on the owner.
var waits = map[string]bool{"question": true, "confirm": true, "human_check": true}

// ends are the kinds of an errand's last event.
var ends = map[string]bool{"done": true, "not_done": true, "failed": true, "stopped": true}

// Channel sends to the owner on one channel; the recipient is always the owner, filled by the
// channel itself, never named by the agent.
type Channel interface {
	Send(ctx context.Context, text string) error
}

// Settings are the owner's choices the hub reads every turn: the agents answering, the primary first
// and then the secondary (none when no agent is added), and whether the owner switched WhatsApp on,
// and the owner's Contacts phone.
type Settings interface {
	Answering(ctx context.Context) (agents []string, whatsapp bool, err error)
	OwnerPhone(ctx context.Context) (string, error)
}

// Config is the hub's: its store and tools, the agents by name, the channels by name, the owner's
// settings, the clock and the owner's time zone, and the console's address for the links the hub
// sends (empty: the path alone).
type Config struct {
	Store      *Store
	Tools      *Tools
	Agents     map[string]agentv1connect.AgentServiceClient
	Channels   map[string]Channel
	Settings   Settings
	Now        func() time.Time
	TimeZone   string
	ConsoleURL string
}

// Hub runs the owner's conversations: each event (the owner's message, an errand's) is stored in its
// conversation as the hub takes it in, and becomes a turn of the primary agent, whose words go to the
// owner on the conversation's channel; of the secondary when the primary can't answer.
//
// Each conversation has a queue its own goroutine drains, one event and its turn at a time, in the
// order the hub took them in; different conversations run in parallel. An event stays pending in the
// store until its turn finishes, so a restart takes it again, and a turn's steps never see the
// events pending after its own. An errand's events queue per run first (one goroutine each, in order)
// while they wait for the run's conversation. Neither Owner nor RunEvent waits on a turn.
//
// A stop comes in two steps: Run's end cancels the turns, and the hub still takes events in (stored
// pending, for the next start) until Close, so the errands the engine's stop ends still reach their
// conversations.
type Hub struct {
	cfg Config
	// base is the turns' context, cancelled when Run ends; life is the intake's, which outlives it:
	// Close ends the intake when nothing is left to take in.
	base, life context.Context
	cancel     context.CancelFunc
	linkWait   time.Duration
	// retryFirst and retryMax are the settings retry's bounds (the consts; tests shorten them).
	retryFirst, retryMax time.Duration

	// intake orders the events taken in: each is stored and queued under it, so the store's order is
	// the queues'; and no idle conversation closes between its opening and its event.
	intake sync.Mutex

	mu    sync.Mutex
	convs map[int64][]*agentv1.Event // a conversation's events to take, while its goroutine runs
	runs  map[int64][]*agentv1.Event // a run's events waiting for its conversation, while its goroutine runs
	// orphans are the runs a full linkWait found no conversation of (a console errand's): their later
	// events are dropped at once, until the run's end.
	orphans map[int64]bool
	// stopping is Run's end: events are still taken in, but no turn starts. closed is Close's: the hub
	// takes nothing more.
	stopping, closed bool
	turns, routes    sync.WaitGroup // the conversations' goroutines, the runs' goroutines
	closeOnce        sync.Once
}

// NewHub is the hub of cfg. It queues again every event still pending, so a turn the engine's last
// stop cut short, or never began, runs now; a tool call that stop left without its result gets one
// saying so first.
func NewHub(cfg Config) *Hub {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	base, cancel := context.WithCancel(context.Background())
	h := &Hub{cfg: cfg, base: base, life: context.WithoutCancel(base), cancel: cancel, linkWait: linkWait,
		retryFirst: retryFirst, retryMax: retryMax,
		convs: map[int64][]*agentv1.Event{}, runs: map[int64][]*agentv1.Event{}, orphans: map[int64]bool{}}
	pending, err := cfg.Store.Pending(base)
	if err != nil {
		slog.Error("converse: reading the events still pending", "err", err)
	}
	closed := map[int64]bool{}
	for _, q := range pending {
		if !closed[q.Conversation] {
			h.closeDangling(q.Conversation)
			closed[q.Conversation] = true
		}
		h.enqueue(q.Conversation, q.Event)
	}
	return h
}

// closeDangling gives each tool call of the conversation that has no result the result that the box
// restarted before it finished, so a resumed turn's agent sees how it ended.
func (h *Hub) closeDangling(conv int64) {
	events, err := h.cfg.Store.Events(h.base, conv, 0)
	if err != nil {
		slog.Error("converse: reading a conversation to resume", "conversation", conv, "err", err)
		return
	}
	var open []*agentv1.ToolCall
	for _, e := range events {
		if c := e.GetToolCall(); c != nil {
			open = append(open, c)
		} else if r := e.GetToolResult(); r != nil {
			if i := slices.IndexFunc(open, func(c *agentv1.ToolCall) bool { return c.GetCallId() == r.GetCallId() }); i >= 0 {
				open = slices.Delete(open, i, i+1)
			}
		}
	}
	for _, c := range open {
		if _, err := h.cfg.Store.Append(h.base, conv, "", &agentv1.Event{Kind: &agentv1.Event_ToolResult{ToolResult: &agentv1.ToolResult{
			CallId: c.GetCallId(), ResultJson: restarted}}}, h.cfg.Now()); err != nil {
			slog.Error("converse: closing a tool call the restart cut short", "conversation", conv, "err", err)
		}
	}
}

// Run closes idle conversations every minute until ctx ends; then it cancels the turns running and
// waits for them, and starts no turn more. A cancelled turn's event stays pending, and so does each
// event taken in after, until Close.
func (h *Hub) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			h.stopping = true
			h.mu.Unlock()
			h.cancel()
			h.turns.Wait()
			return
		case <-tick.C:
			h.intake.Lock()
			h.closeIdle(h.base)
			h.intake.Unlock()
		}
	}
}

// Close ends the hub's intake once what may still tell it (the errands the stop ends) has: it takes
// nothing more, and waits for the events being taken in (each stored, pending) and any turn still
// running.
func (h *Hub) Close() {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		h.mu.Unlock()
		h.cancel()
		h.routes.Wait()
		h.turns.Wait()
	})
}

// Owner takes in a message of the owner's, already gated as theirs, on the channel's thread: it
// closes the idle conversations first, so one idle too long is never reused, opens the thread's
// conversation, stores the message there and queues it. It returns once the message is stored and
// queued; the turn runs on the conversation's own goroutine. src is the connector's message it came
// from: one taken in before (a listener's replay) is taken in nothing more.
func (h *Hub) Owner(ctx context.Context, channel, thread, text string, src Source) {
	h.intake.Lock()
	defer h.intake.Unlock()
	if heard, err := h.cfg.Store.Heard(ctx, src); err != nil || heard {
		if err != nil {
			slog.Error("converse: looking for a message taken in", "channel", channel, "err", err)
		}
		return
	}
	h.closeIdle(ctx)
	c, err := h.cfg.Store.Open(ctx, channel, thread, h.cfg.Now())
	if err != nil {
		slog.Error("converse: opening a conversation", "channel", channel, "err", err)
		return
	}
	h.takeIn(ctx, c.ID, &agentv1.Event{Kind: &agentv1.Event_Owner{Owner: text}}, src)
}

// LastHeard is when the connector stored the channel's newest message the hub took in; false when
// none was.
func (h *Hub) LastHeard(ctx context.Context, channel string) (time.Time, bool, error) {
	return h.cfg.Store.LastHeard(ctx, channel)
}

// RunEvent is signins' Config.Events: it queues what an errand tells and returns at once. The event
// goes to the conversation that started the run, waiting up to linkWait for the run to be linked;
// a run no conversation started is dropped.
func (h *Hub) RunEvent(e signins.RunEvent) {
	ev := &agentv1.Event{Kind: &agentv1.Event_Errand{Errand: &agentv1.ErrandEvent{
		Run: e.Run, Kind: e.Kind, Text: e.Text, Options: append([]string{}, e.Options...), Wait: e.Wait}}}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	q, busy := h.runs[e.Run]
	h.runs[e.Run] = append(q, ev)
	if !busy {
		h.routes.Add(1)
		go h.routeRun(e.Run)
	}
}

// takeIn stores the event from src in the conversation, pending, and queues it there. Called with
// h.intake held.
func (h *Hub) takeIn(ctx context.Context, conv int64, e *agentv1.Event, src Source) {
	if _, err := h.cfg.Store.Intake(ctx, conv, e, h.cfg.Now(), src); err != nil {
		slog.Error("converse: keeping an event", "conversation", conv, "err", err)
		return
	}
	h.enqueue(conv, e)
}

// idle is the hub with nothing queued and no turn running.
func (h *Hub) idle() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.convs) == 0 && len(h.runs) == 0
}

func (h *Hub) closeIdle(ctx context.Context) {
	if err := h.cfg.Store.CloseIdle(ctx, h.cfg.Now(), h.running); err != nil {
		slog.Error("converse: closing idle conversations", "err", err)
	}
}

// running is a run still running; one that can't be read counts as running, so its conversation
// stays open.
func (h *Hub) running(run int64) bool {
	ok, err := h.cfg.Tools.Running(h.life, run)
	if err != nil {
		slog.Error("converse: reading a run", "run", run, "err", err)
		return true
	}
	return ok
}

// enqueue queues the event on its conversation, starting the conversation's goroutine when none runs.
// Once the hub stops it queues nothing: the event, stored pending, is the next start's.
func (h *Hub) enqueue(conv int64, e *agentv1.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopping || h.closed {
		return
	}
	q, busy := h.convs[conv]
	h.convs[conv] = append(q, e)
	if !busy {
		h.turns.Add(1)
		go h.drain(conv)
	}
}

// next pops the head of a queue, and forgets the queue (its goroutine ends) when it is empty.
func (h *Hub) next(queues map[int64][]*agentv1.Event, key int64) (*agentv1.Event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	q := queues[key]
	if len(q) == 0 {
		delete(queues, key)
		return nil, false
	}
	queues[key] = q[1:]
	return q[0], true
}

// drain takes the conversation's events in order, each with its turn.
func (h *Hub) drain(conv int64) {
	defer h.turns.Done()
	for {
		e, ok := h.next(h.convs, conv)
		if !ok {
			return
		}
		h.take(conv, e)
	}
}

// routeRun takes in the run's events, in order, in the conversation that started the run. A run
// none started costs one wait: it is remembered as an orphan until its end.
func (h *Hub) routeRun(run int64) {
	defer h.routes.Done()
	var conv int64
	looked := false
	for {
		e, ok := h.next(h.runs, run)
		if !ok {
			return
		}
		if !looked {
			conv, looked = h.conversationOf(run), true
		}
		if conv == 0 {
			slog.Info("converse: an errand's event of no conversation, dropped", "run", run, "kind", e.GetErrand().GetKind())
			h.orphan(run, !ends[e.GetErrand().GetKind()])
			continue
		}
		h.intake.Lock()
		h.takeIn(h.life, conv, e, Source{})
		h.intake.Unlock()
	}
}

// orphan remembers the run as no conversation's, or forgets it (its end).
func (h *Hub) orphan(run int64, is bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !is {
		delete(h.orphans, run)
		return
	}
	if len(h.orphans) >= noConversationCap {
		clear(h.orphans)
	}
	h.orphans[run] = true
}

// conversationOf is the conversation that started the run, looked for until linkWait passes; 0 when
// none did, at once for an orphan.
func (h *Hub) conversationOf(run int64) int64 {
	h.mu.Lock()
	orphan := h.orphans[run]
	h.mu.Unlock()
	if orphan {
		return 0
	}
	deadline := time.Now().Add(h.linkWait)
	for {
		conv, ok, err := h.cfg.Store.ConversationOfRun(h.life, run)
		if err != nil {
			slog.Error("converse: the conversation of a run", "run", run, "err", err)
		}
		if ok {
			return conv
		}
		if !time.Now().Before(deadline) {
			return 0
		}
		time.Sleep(linkPoll)
	}
}

// take answers an event taken in: a human check with the console's link to the errand, a Confirm by
// sending it to the owner as the site said it, anything else with a turn of the agents answering;
// nothing at all while the conversation's channel is off or no agent is added. The event stops being pending
// once answered, unless the hub's stop cut its turn short.
func (h *Hub) take(conv int64, e *agentv1.Event) {
	ctx := h.base
	if ctx.Err() != nil { // the hub stopped: the event stays pending
		return
	}
	c, err := h.cfg.Store.conversation(ctx, conv)
	if err != nil {
		slog.Error("converse: reading a conversation", "conversation", conv, "err", err)
		return
	}
	if !h.answer(ctx, c, e) {
		return
	}
	if err := h.cfg.Store.Taken(context.WithoutCancel(ctx), e.GetId()); err != nil {
		slog.Error("converse: marking an event taken", "conversation", conv, "err", err)
	}
}

// answer answers the event; false when the hub's stop cut it short. Settings that can't be read are
// read again (the event waits, pending, in its queue), never taken for switched off. An errand's
// wait that ended before its turn (answered in the console, or its run gone with a restart) is told
// nothing: it asks nothing any more.
func (h *Hub) answer(ctx context.Context, c Conversation, e *agentv1.Event) bool {
	order, ch, err := h.answering(ctx, c)
	for delay := h.retryFirst; err != nil; delay = min(2*delay, h.retryMax) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
		order, ch, err = h.answering(ctx, c)
	}
	if x := e.GetErrand(); waits[x.GetKind()] && !h.cfg.Tools.Waits(x.GetRun(), x.GetKind(), x.GetWait()) {
		slog.Info("converse: an errand's wait that ended, not told", "conversation", c.ID, "run", x.GetRun(), "kind", x.GetKind())
		return true
	}
	if len(order) == 0 {
		return true
	}
	switch x := e.GetErrand(); x.GetKind() {
	case "human_check":
		h.say(ctx, c, ch, "", humanCheckWords+": "+h.errandLink(x.GetRun()))
		return true
	case "confirm":
		h.relayConfirm(ctx, c, ch, e.GetId(), x)
		return true
	}
	return h.turn(ctx, c, e, order, ch)
}

// relayConfirm sends the owner the errand's Confirm as the site said it, its options with it, and
// only once it was sent marks it told: a Confirm the owner never got is never answerable.
func (h *Hub) relayConfirm(ctx context.Context, c Conversation, ch Channel, id int64, x *agentv1.ErrandEvent) {
	words := strings.Join(append([]string{x.GetText()}, x.GetOptions()...), "\n")
	if !h.say(ctx, c, ch, "", words) {
		return
	}
	if err := h.cfg.Store.tellConfirm(ctx, c.ID, id); err != nil {
		slog.Error("converse: marking a Confirm told", "conversation", c.ID, "run", x.GetRun(), "err", err)
	}
}

// answering is the agents answering, in order, and the conversation's channel, when the owner
// switched that channel on and added an agent; none when not. An error is settings that could not be
// read: not an answer either way.
func (h *Hub) answering(ctx context.Context, c Conversation) ([]string, Channel, error) {
	sctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	order, whatsapp, err := h.cfg.Settings.Answering(sctx)
	if err != nil {
		slog.Error("converse: reading the agent's settings", "conversation", c.ID, "err", err)
		return nil, nil, err
	}
	if len(order) == 0 || !switchedOn(c.Channel, whatsapp) {
		return nil, nil, nil
	}
	ch, ok := h.cfg.Channels[c.Channel]
	if !ok {
		slog.Error("converse: no such channel", "conversation", c.ID, "channel", c.Channel)
		return nil, nil, nil
	}
	return order, ch, nil
}

// switchedOn is the owner's "Answer me on" switch of the channel; WhatsApp has the only one yet.
func switchedOn(channel string, whatsapp bool) bool {
	return channel == "whatsapp" && whatsapp
}

// errandLink is the console's page of the run.
func (h *Hub) errandLink(run int64) string {
	return strings.TrimRight(h.cfg.ConsoleURL, "/") + "/errands/runs/" + strconv.FormatInt(run, 10)
}

// turn steps the first agent of the order on the event e until it says something or ends, running
// each tool it calls, at most MaxTools; false when the hub's stop cut it short (nothing said: it runs
// again). A step the agent can't answer (fallsBack) goes to the next agent of the order, on the same
// window; the owner is told of the switch unless the next agent said the conversation's last words
// already (once a stretch). A turn that fails tells the owner what it could (failed), and why when no
// agent could answer.
func (h *Hub) turn(ctx context.Context, c Conversation, e *agentv1.Event, order []string, ch Channel) bool {
	taking := e.GetId()
	cur := 0
	for tools := 0; ; {
		name := order[cur]
		resp, err := h.step(ctx, c, taking, name)
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			if !fallsBack(err) {
				h.failed(ctx, c, ch, e, "")
				return true
			}
			code := connect.CodeOf(err)
			if code == connect.CodeResourceExhausted {
				if err := h.cfg.Store.MarkLimited(ctx, name, h.cfg.Now()); err != nil {
					slog.Error("converse: marking an agent's limit", "agent", name, "err", err)
				}
			}
			if cur+1 == len(order) {
				h.failed(ctx, c, ch, e, cantAnswer(name, code))
				return true
			}
			next := order[cur+1]
			if h.noticeDue(ctx, c.ID, next) {
				h.say(ctx, c, ch, "", switchNotice(name, next, code))
			}
			cur++
			continue
		}
		switch a := resp.GetAction().(type) {
		case *agentv1.StepResponse_ToolCall:
			if tools == MaxTools {
				slog.Warn("converse: a turn ran out of tool calls", "conversation", c.ID, "tools", MaxTools)
				h.failed(ctx, c, ch, e, "")
				return true
			}
			tools++
			if !h.runTool(ctx, c.ID, taking, name, a.ToolCall) {
				if ctx.Err() != nil {
					return false
				}
				h.failed(ctx, c, ch, e, "")
				return true
			}
		case *agentv1.StepResponse_Say:
			h.say(ctx, c, ch, name, a.Say)
			return true
		case *agentv1.StepResponse_End:
			h.say(ctx, c, ch, name, a.End)
			if err := h.cfg.Store.End(ctx, c.ID, h.running); err != nil {
				slog.Error("converse: ending a conversation", "conversation", c.ID, "err", err)
			}
			return true
		default:
			slog.Error("converse: a step with no action", "conversation", c.ID)
			h.failed(ctx, c, ch, e, "")
			return true
		}
	}
}

// noticeDue is a switch to the agent next the owner is not told of yet: another agent said the
// conversation's last words, or none did. A store that can't tell counts as due.
func (h *Hub) noticeDue(ctx context.Context, conv int64, next string) bool {
	last, err := h.cfg.Store.LastAgent(ctx, conv)
	if err != nil {
		slog.Error("converse: reading who answered last", "conversation", conv, "err", err)
		return true
	}
	return last != next
}

// failed tells the owner what a failed turn on the event could not: an errand's question still
// waiting, as the site asked it with its options, or an errand's end in the errand's own words, as a
// Confirm is relayed; anything else, that the agent couldn't finish, and why when reason says (no
// agent could answer). A question told this way is answered, as any is, only with one of its options
// or the owner's own message (answer_errand).
func (h *Hub) failed(ctx context.Context, c Conversation, ch Channel, e *agentv1.Event, reason string) {
	x := e.GetErrand()
	switch {
	case x.GetKind() == "question" && h.cfg.Tools.Waits(x.GetRun(), x.GetKind(), x.GetWait()):
		h.say(ctx, c, ch, "", strings.Join(append([]string{x.GetText()}, x.GetOptions()...), "\n"))
	case ends[x.GetKind()] && strings.TrimSpace(x.GetText()) != "":
		h.say(ctx, c, ch, "", x.GetText())
	case reason != "":
		h.say(ctx, c, ch, "", couldNotFinish+". "+reason)
	default:
		h.say(ctx, c, ch, "", couldNotFinish)
	}
}

// step is the named agent's next action on the conversation's window for the event #taking, its
// usage added up as the agent's and its limit, if it had one, cleared. A failed step's error is
// logged by its code alone (a daemon's error may quote the model's words); an agent not configured
// fails as not reachable.
func (h *Hub) step(ctx context.Context, c Conversation, taking int64, name string) (*agentv1.StepResponse, error) {
	agent, ok := h.cfg.Agents[name]
	if !ok {
		slog.Error("converse: an agent answering is not configured", "conversation", c.ID, "agent", name)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the agent is not configured"))
	}
	events, err := h.cfg.Store.Window(ctx, c.ID, SeenEvents, taking)
	if err != nil {
		slog.Error("converse: reading a conversation's events", "conversation", c.ID, "err", err)
		return nil, err
	}
	stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	resp, err := agent.Step(stepCtx, connect.NewRequest(&agentv1.StepRequest{Channel: c.Channel, Events: events,
		Tools: h.cfg.Tools.Specs(), Now: timestamppb.New(h.cfg.Now()), TimeZone: h.cfg.TimeZone}))
	if err != nil {
		slog.Error("converse: a step failed", "conversation", c.ID, "agent", name, "code", connect.CodeOf(err).String())
		return nil, err
	}
	if err := h.cfg.Store.AddUsage(ctx, c.ID, name, resp.Msg.GetUsage()); err != nil {
		slog.Error("converse: adding a step's usage", "conversation", c.ID, "err", err)
	}
	if err := h.cfg.Store.ClearLimited(ctx, name); err != nil {
		slog.Error("converse: clearing an agent's limit", "agent", name, "err", err)
	}
	return resp.Msg, nil
}

// runTool appends the agent's call, runs it in the turn taking #taking and appends its result as the
// agent's; false when either can't be kept.
func (h *Hub) runTool(ctx context.Context, conv, taking int64, agent string, call *agentv1.ToolCall) bool {
	if _, err := h.cfg.Store.Append(ctx, conv, agent, &agentv1.Event{Kind: &agentv1.Event_ToolCall{ToolCall: call}}, h.cfg.Now()); err != nil {
		slog.Error("converse: keeping a tool call", "conversation", conv, "err", err)
		return false
	}
	result := h.cfg.Tools.Call(ctx, conv, taking, call)
	if _, err := h.cfg.Store.Append(ctx, conv, agent, &agentv1.Event{Kind: &agentv1.Event_ToolResult{ToolResult: &agentv1.ToolResult{
		CallId: call.GetCallId(), ResultJson: result}}}, h.cfg.Now()); err != nil {
		slog.Error("converse: keeping a tool's result", "conversation", conv, "err", err)
		return false
	}
	return true
}

// say keeps the words as the agent's ("" for the hub's own) and sends them to the owner on the
// conversation's channel; false when the send failed.
func (h *Hub) say(ctx context.Context, c Conversation, ch Channel, agent, text string) bool {
	if _, err := h.cfg.Store.Append(ctx, c.ID, agent, &agentv1.Event{Kind: &agentv1.Event_Agent{Agent: text}}, h.cfg.Now()); err != nil {
		slog.Error("converse: keeping the agent's words", "conversation", c.ID, "err", err)
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := ch.Send(sendCtx, text); err != nil {
		slog.Error("converse: sending to the owner", "conversation", c.ID, "channel", c.Channel, "err", err)
		return false
	}
	return true
}
