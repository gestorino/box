package converse

import (
	"context"
	"log/slog"
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
)

// Channel sends to the owner on one channel; the recipient is always the owner, filled by the
// channel itself, never named by the agent.
type Channel interface {
	Send(ctx context.Context, text string) error
}

// Settings are the owner's choices the hub reads every turn: the chosen agent ("" for none) and
// whether the owner switched WhatsApp on, and the owner's Contacts phone.
type Settings interface {
	Agent(ctx context.Context) (name string, whatsapp bool, err error)
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
// conversation as the hub takes it in, and becomes a turn of the chosen agent, whose words go to the
// owner on the conversation's channel.
//
// Each conversation has a queue its own goroutine drains, one event and its turn at a time, in the
// order the hub took them in; different conversations run in parallel. An event stays pending in the
// store until its turn finishes, so a restart takes it again, and a turn's steps never see the
// events pending after its own. An errand's events queue per run first (one goroutine each, in order)
// while they wait for the run's conversation. Neither Owner nor RunEvent waits on a turn.
type Hub struct {
	cfg      Config
	base     context.Context
	cancel   context.CancelFunc
	linkWait time.Duration

	// intake orders the events taken in: each is stored and queued under it, so the store's order is
	// the queues'; and no idle conversation closes between its opening and its event.
	intake sync.Mutex

	mu     sync.Mutex
	convs  map[int64][]*agentv1.Event // a conversation's events to take, while its goroutine runs
	runs   map[int64][]*agentv1.Event // a run's events waiting for its conversation, while its goroutine runs
	closed bool
	wg     sync.WaitGroup
}

// NewHub is the hub of cfg. It queues again every event still pending, so a turn the engine's last
// stop cut short, or never began, runs now.
func NewHub(cfg Config) *Hub {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	base, cancel := context.WithCancel(context.Background())
	h := &Hub{cfg: cfg, base: base, cancel: cancel, linkWait: linkWait,
		convs: map[int64][]*agentv1.Event{}, runs: map[int64][]*agentv1.Event{}}
	pending, err := cfg.Store.Pending(base)
	if err != nil {
		slog.Error("converse: reading the events still pending", "err", err)
	}
	for _, q := range pending {
		h.enqueue(q.Conversation, q.Event)
	}
	return h
}

// Run closes idle conversations every minute until ctx ends; then it cancels the turns running and
// waits for them, and the hub takes nothing more. A cancelled turn's event stays pending.
func (h *Hub) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			h.closed = true
			h.mu.Unlock()
			h.cancel()
			h.wg.Wait()
			return
		case <-tick.C:
			h.intake.Lock()
			h.closeIdle(h.base)
			h.intake.Unlock()
		}
	}
}

// Owner takes in a message of the owner's, already gated as theirs, on the channel's thread: it
// closes the idle conversations first, so one idle too long is never reused, opens the thread's
// conversation, stores the message there and queues it. It returns once the message is stored and
// queued; the turn runs on the conversation's own goroutine.
func (h *Hub) Owner(ctx context.Context, channel, thread, text string) {
	h.intake.Lock()
	defer h.intake.Unlock()
	h.closeIdle(ctx)
	c, err := h.cfg.Store.Open(ctx, channel, thread, h.cfg.Now())
	if err != nil {
		slog.Error("converse: opening a conversation", "channel", channel, "err", err)
		return
	}
	h.takeIn(ctx, c.ID, &agentv1.Event{Kind: &agentv1.Event_Owner{Owner: text}})
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
		h.wg.Add(1)
		go h.routeRun(e.Run)
	}
}

// takeIn stores the event in the conversation, pending, and queues it there. Called with h.intake held.
func (h *Hub) takeIn(ctx context.Context, conv int64, e *agentv1.Event) {
	if _, err := h.cfg.Store.Intake(ctx, conv, e, h.cfg.Now()); err != nil {
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
	ok, err := h.cfg.Tools.Running(h.base, run)
	if err != nil {
		slog.Error("converse: reading a run", "run", run, "err", err)
		return true
	}
	return ok
}

// enqueue queues the event on its conversation, starting the conversation's goroutine when none runs.
func (h *Hub) enqueue(conv int64, e *agentv1.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	q, busy := h.convs[conv]
	h.convs[conv] = append(q, e)
	if !busy {
		h.wg.Add(1)
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
	defer h.wg.Done()
	for {
		e, ok := h.next(h.convs, conv)
		if !ok {
			return
		}
		h.take(conv, e)
	}
}

// routeRun takes in the run's events, in order, in the conversation that started the run; it looks
// for that conversation once per goroutine, so a run none started costs one wait, not one per event.
func (h *Hub) routeRun(run int64) {
	defer h.wg.Done()
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
			continue
		}
		h.intake.Lock()
		h.takeIn(h.base, conv, e)
		h.intake.Unlock()
	}
}

// conversationOf is the conversation that started the run, looked for until linkWait passes; 0 when
// none did.
func (h *Hub) conversationOf(run int64) int64 {
	deadline := time.Now().Add(h.linkWait)
	for {
		conv, ok, err := h.cfg.Store.ConversationOfRun(h.base, run)
		if err != nil {
			slog.Error("converse: the conversation of a run", "run", run, "err", err)
		}
		if ok {
			return conv
		}
		if !time.Now().Before(deadline) {
			return 0
		}
		select {
		case <-h.base.Done():
			return 0
		case <-time.After(linkPoll):
		}
	}
}

// take answers an event taken in: a human check with the console's link to the errand, a Confirm by
// sending it to the owner as the site said it, anything else with a turn of the chosen agent; nothing
// at all while the conversation's channel is off or no agent is chosen. The event stops being pending
// once answered, unless the hub's stop cut its turn short.
func (h *Hub) take(conv int64, e *agentv1.Event) {
	ctx := h.base
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

// answer answers the event; false when the hub's stop cut it short.
func (h *Hub) answer(ctx context.Context, c Conversation, e *agentv1.Event) bool {
	name, ch, ok := h.answering(ctx, c)
	if !ok {
		return ctx.Err() == nil
	}
	switch x := e.GetErrand(); x.GetKind() {
	case "human_check":
		h.say(ctx, c, ch, humanCheckWords+": "+h.errandLink(x.GetRun()))
		return true
	case "confirm":
		h.relayConfirm(ctx, c, ch, e.GetId(), x)
		return true
	}
	return h.turn(ctx, c, e.GetId(), name, ch)
}

// relayConfirm sends the owner the errand's Confirm as the site said it, its options with it, and
// only once it was sent marks it told: a Confirm the owner never got is never answerable.
func (h *Hub) relayConfirm(ctx context.Context, c Conversation, ch Channel, id int64, x *agentv1.ErrandEvent) {
	words := strings.Join(append([]string{x.GetText()}, x.GetOptions()...), "\n")
	if !h.say(ctx, c, ch, words) {
		return
	}
	if err := h.cfg.Store.tellConfirm(ctx, c.ID, id); err != nil {
		slog.Error("converse: marking a Confirm told", "conversation", c.ID, "run", x.GetRun(), "err", err)
	}
}

// answering is the chosen agent and the conversation's channel, when the owner switched that
// channel on and chose an agent.
func (h *Hub) answering(ctx context.Context, c Conversation) (string, Channel, bool) {
	sctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	name, whatsapp, err := h.cfg.Settings.Agent(sctx)
	if err != nil {
		slog.Error("converse: reading the agent's settings", "conversation", c.ID, "err", err)
		return "", nil, false
	}
	if name == "" || !switchedOn(c.Channel, whatsapp) {
		return "", nil, false
	}
	ch, ok := h.cfg.Channels[c.Channel]
	if !ok {
		slog.Error("converse: no such channel", "conversation", c.ID, "channel", c.Channel)
		return "", nil, false
	}
	return name, ch, true
}

// switchedOn is the owner's "Answer me on" switch of the channel; WhatsApp has the only one yet.
func switchedOn(channel string, whatsapp bool) bool {
	return channel == "whatsapp" && whatsapp
}

// errandLink is the console's page of the run.
func (h *Hub) errandLink(run int64) string {
	return strings.TrimRight(h.cfg.ConsoleURL, "/") + "/errands/runs/" + strconv.FormatInt(run, 10)
}

// turn steps the agent on the event #taking until it says something or ends, running each tool it
// calls, at most MaxTools; false when the hub's stop cut it short (nothing said: it runs again).
func (h *Hub) turn(ctx context.Context, c Conversation, taking int64, name string, ch Channel) bool {
	agent, ok := h.cfg.Agents[name]
	if !ok {
		slog.Error("converse: the chosen agent is not configured", "conversation", c.ID, "agent", name)
		h.say(ctx, c, ch, couldNotFinish)
		return true
	}
	for tools := 0; ; {
		resp, ok := h.step(ctx, c, taking, agent)
		if !ok {
			if ctx.Err() != nil {
				return false
			}
			h.say(ctx, c, ch, couldNotFinish)
			return true
		}
		switch a := resp.GetAction().(type) {
		case *agentv1.StepResponse_ToolCall:
			if tools == MaxTools {
				slog.Warn("converse: a turn ran out of tool calls", "conversation", c.ID, "tools", MaxTools)
				h.say(ctx, c, ch, couldNotFinish)
				return true
			}
			tools++
			if !h.runTool(ctx, c.ID, a.ToolCall) {
				if ctx.Err() != nil {
					return false
				}
				h.say(ctx, c, ch, couldNotFinish)
				return true
			}
		case *agentv1.StepResponse_Say:
			h.say(ctx, c, ch, a.Say)
			return true
		case *agentv1.StepResponse_End:
			h.say(ctx, c, ch, a.End)
			if err := h.cfg.Store.End(ctx, c.ID, h.running); err != nil {
				slog.Error("converse: ending a conversation", "conversation", c.ID, "err", err)
			}
			return true
		default:
			slog.Error("converse: a step with no action", "conversation", c.ID)
			h.say(ctx, c, ch, couldNotFinish)
			return true
		}
	}
}

// step is the agent's next action on the conversation's window for the event #taking, its usage
// added up; false when the step failed (logged by its code alone: a daemon's error may quote the
// model's words).
func (h *Hub) step(ctx context.Context, c Conversation, taking int64, agent agentv1connect.AgentServiceClient) (*agentv1.StepResponse, bool) {
	events, err := h.cfg.Store.Window(ctx, c.ID, SeenEvents, taking)
	if err != nil {
		slog.Error("converse: reading a conversation's events", "conversation", c.ID, "err", err)
		return nil, false
	}
	stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	resp, err := agent.Step(stepCtx, connect.NewRequest(&agentv1.StepRequest{Channel: c.Channel, Events: events,
		Tools: h.cfg.Tools.Specs(), Now: timestamppb.New(h.cfg.Now()), TimeZone: h.cfg.TimeZone}))
	if err != nil {
		slog.Error("converse: a step failed", "conversation", c.ID, "code", connect.CodeOf(err).String())
		return nil, false
	}
	if err := h.cfg.Store.AddUsage(ctx, c.ID, resp.Msg.GetUsage()); err != nil {
		slog.Error("converse: adding a step's usage", "conversation", c.ID, "err", err)
	}
	return resp.Msg, true
}

// runTool appends the call, runs it and appends its result; false when either can't be kept.
func (h *Hub) runTool(ctx context.Context, conv int64, call *agentv1.ToolCall) bool {
	if _, err := h.cfg.Store.Append(ctx, conv, &agentv1.Event{Kind: &agentv1.Event_ToolCall{ToolCall: call}}, h.cfg.Now()); err != nil {
		slog.Error("converse: keeping a tool call", "conversation", conv, "err", err)
		return false
	}
	result := h.cfg.Tools.Call(ctx, conv, call)
	if _, err := h.cfg.Store.Append(ctx, conv, &agentv1.Event{Kind: &agentv1.Event_ToolResult{ToolResult: &agentv1.ToolResult{
		CallId: call.GetCallId(), ResultJson: result}}}, h.cfg.Now()); err != nil {
		slog.Error("converse: keeping a tool's result", "conversation", conv, "err", err)
		return false
	}
	return true
}

// say keeps the words as the agent's and sends them to the owner on the conversation's channel;
// false when the send failed.
func (h *Hub) say(ctx context.Context, c Conversation, ch Channel, text string) bool {
	if _, err := h.cfg.Store.Append(ctx, c.ID, &agentv1.Event{Kind: &agentv1.Event_Agent{Agent: text}}, h.cfg.Now()); err != nil {
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
