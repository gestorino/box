package converse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "concierge/engine/internal/gen/agent/v1"
	"concierge/engine/internal/gen/agent/v1/agentv1connect"
	"concierge/engine/internal/h2c"
	"concierge/engine/internal/signins"
)

// fakeAgent is an agent daemon: its step is the test's script, and it keeps every request it got.
type fakeAgent struct {
	agentv1connect.UnimplementedAgentServiceHandler
	step func(n int, req *agentv1.StepRequest) (*agentv1.StepResponse, error)

	mu    sync.Mutex
	steps []*agentv1.StepRequest
}

func (a *fakeAgent) Step(_ context.Context, req *connect.Request[agentv1.StepRequest]) (*connect.Response[agentv1.StepResponse], error) {
	a.mu.Lock()
	a.steps = append(a.steps, req.Msg)
	n := len(a.steps)
	a.mu.Unlock()
	resp, err := a.step(n, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (a *fakeAgent) requests() []*agentv1.StepRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*agentv1.StepRequest(nil), a.steps...)
}

// sockDir is a short temporary directory for a test's socket (macOS caps a socket's path).
func sockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// client is an agent client of the socket, as the engine dials its agents.
func client(sock string) agentv1connect.AgentServiceClient {
	return agentv1connect.NewAgentServiceClient(&http.Client{Transport: h2c.Transport(sock)}, "http://agent")
}

// serveAgent serves the fake agent on a real Unix socket, HTTP/2 only, and is its client.
func serveAgent(t *testing.T, a *fakeAgent) agentv1connect.AgentServiceClient {
	t.Helper()
	sock := filepath.Join(sockDir(t), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewAgentServiceHandler(a))
	srv := &http.Server{Handler: mux, Protocols: h2c.Protocols()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return client(sock)
}

// fakeChannel is the owner's channel: what was sent to them, in order.
type fakeChannel struct {
	sent chan string
	err  error
}

func newChannel() *fakeChannel { return &fakeChannel{sent: make(chan string, 100)} }

func (c *fakeChannel) Send(_ context.Context, text string) error {
	if c.err != nil {
		return c.err
	}
	c.sent <- text
	return nil
}

// next is the next message sent to the owner.
func (c *fakeChannel) next(t *testing.T) string {
	t.Helper()
	select {
	case s := <-c.sent:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was sent to the owner")
		return ""
	}
}

// none is nothing more sent to the owner.
func (c *fakeChannel) none(t *testing.T) {
	t.Helper()
	select {
	case s := <-c.sent:
		t.Fatalf("sent %q", s)
	default:
	}
}

type fakeSettings struct {
	mu       sync.Mutex
	agents   []string
	whatsapp bool
	// failing is how many of the next reads fail, as hostd away does; reads counts them all.
	failing, reads int
}

func (s *fakeSettings) Answering(context.Context) ([]string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.failing > 0 {
		s.failing--
		return nil, false, errors.New("hostd away")
	}
	return append([]string(nil), s.agents...), s.whatsapp, nil
}

// fail makes the next n reads fail.
func (s *fakeSettings) fail(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = n
}

func (s *fakeSettings) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// set is the WhatsApp switch and the agents answering, in order.
func (s *fakeSettings) set(whatsapp bool, agents ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agents, s.whatsapp = agents, whatsapp
}
func (s *fakeSettings) OwnerPhone(context.Context) (string, error) { return "+351900000000", nil }

// clock is a test's time, moved by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type rig struct {
	hub      *Hub
	store    *Store
	agent    *fakeAgent // claude
	codex    *fakeAgent // nil but in a rig of two agents
	channel  *fakeChannel
	settings *fakeSettings
	errands  *fakeErrands
	clock    *clock
	// stop ends the hub's Run and waits for it, as the engine's stop does.
	stop func()
}

type stepFunc = func(n int, req *agentv1.StepRequest) (*agentv1.StepResponse, error)

func newRig(t *testing.T, store *Store, step stepFunc) *rig {
	t.Helper()
	return newRigOf(t, store, step, nil)
}

// newRigOf is a rig of claude stepping as step and, when codex isn't nil, codex stepping as codex:
// claude answers, codex when claude can't.
func newRigOf(t *testing.T, store *Store, step, codex stepFunc) *rig {
	t.Helper()
	r := &rig{store: store, agent: &fakeAgent{step: step}, channel: newChannel(),
		settings: &fakeSettings{agents: []string{"claude"}, whatsapp: true}, errands: newFake(), clock: &clock{now: t0}}
	agents := map[string]agentv1connect.AgentServiceClient{"claude": serveAgent(t, r.agent)}
	if codex != nil {
		r.codex = &fakeAgent{step: codex}
		agents["codex"] = serveAgent(t, r.codex)
		r.settings.agents = []string{"claude", "codex"}
	}
	r.hub = NewHub(Config{Store: store, Tools: NewTools(r.errands, store),
		Agents:   agents,
		Channels: map[string]Channel{"whatsapp": r.channel}, Settings: r.settings, Now: r.clock.Now,
		TimeZone: "Europe/Lisbon", ConsoleURL: "http://box.local"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.hub.Run(ctx); close(done) }()
	r.stop = sync.OnceFunc(func() { cancel(); <-done })
	t.Cleanup(func() { r.stop(); r.hub.Close() })
	return r
}

// track counts a step in flight and keeps the most ever at once.
func track(inFlight, most *atomic.Int32) {
	n := inFlight.Add(1)
	for {
		m := most.Load()
		if n <= m || most.CompareAndSwap(m, n) {
			return
		}
	}
}

// lastOwner is the newest owner's message a step saw, and its id.
func lastOwner(req *agentv1.StepRequest) (string, int64) {
	var text string
	var id int64
	for _, e := range req.GetEvents() {
		if o, ok := e.GetKind().(*agentv1.Event_Owner); ok {
			text, id = o.Owner, e.GetId()
		}
	}
	return text, id
}

func toolCall(name, args string) *agentv1.StepResponse {
	return &agentv1.StepResponse{Action: &agentv1.StepResponse_ToolCall{ToolCall: &agentv1.ToolCall{
		CallId: "c1", Name: name, ArgumentsJson: args}}, Usage: &agentv1.Usage{Model: "m", InputTokens: 10, OutputTokens: 2, CostUsd: 0.01, CostKnown: true}}
}

func say(text string) *agentv1.StepResponse {
	return &agentv1.StepResponse{Action: &agentv1.StepResponse_Say{Say: text},
		Usage: &agentv1.Usage{Model: "m", InputTokens: 20, OutputTokens: 3, CostUsd: 0.02, CostKnown: true}}
}

// settle waits until the hub has nothing queued and no turn running.
func (r *rig) settle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !r.hub.idle() {
		if time.Now().After(deadline) {
			t.Fatal("the hub never settled")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// only is the one conversation of the store.
func (r *rig) only(t *testing.T) (Conversation, []*agentv1.Event) {
	t.Helper()
	list, err := r.store.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("conversations: %+v", list)
	}
	c, events, _, err := r.store.One(context.Background(), list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return c, plain(events)
}

// plain is the events without their agent and time.
func plain(stored []StoredEvent) []*agentv1.Event {
	out := make([]*agentv1.Event, len(stored))
	for i, se := range stored {
		out[i] = se.Event
	}
	return out
}

// kinds is each event's kind, in order.
func kinds(events []*agentv1.Event) []string {
	out := []string{}
	for _, e := range events {
		switch k := e.GetKind().(type) {
		case *agentv1.Event_Owner:
			out = append(out, "owner")
		case *agentv1.Event_Agent:
			out = append(out, "agent")
		case *agentv1.Event_ToolCall:
			out = append(out, "tool_call")
		case *agentv1.Event_ToolResult:
			out = append(out, "tool_result")
		case *agentv1.Event_Errand:
			out = append(out, "errand:"+k.Errand.GetKind())
		}
	}
	return out
}

func equal(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func TestATurnLoopsThroughToolsToASay(t *testing.T) {
	r := newRig(t, openStore(t), func(n int, _ *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		if n == 1 {
			return toolCall("list_accounts", `{}`), nil
		}
		return say("On it"), nil
	})
	r.hub.Owner(context.Background(), "whatsapp", "chat", "book something", Source{})
	if got := r.channel.next(t); got != "On it" {
		t.Fatalf("sent %q", got)
	}
	r.settle(t)
	r.channel.none(t)
	c, events := r.only(t)
	if want := []string{"owner", "tool_call", "tool_result", "agent"}; !equal(kinds(events), want) {
		t.Fatalf("events %v, want %v", kinds(events), want)
	}
	if events[0].GetOwner() != "book something" || events[1].GetToolCall().GetName() != "list_accounts" ||
		events[2].GetToolResult().GetCallId() != "c1" || events[3].GetAgent() != "On it" {
		t.Fatalf("events: %v", events)
	}
	if u := c.Total(""); u.InTokens != 30 || u.OutTokens != 5 || !u.CostKnown || u.CostUSD < 0.0299 || u.CostUSD > 0.0301 || u.Model != "m" {
		t.Fatalf("usage: %+v", c)
	}
	steps := r.agent.requests()
	if len(steps) != 2 {
		t.Fatalf("steps: %d", len(steps))
	}
	second := steps[1]
	if second.GetChannel() != "whatsapp" || second.GetTimeZone() != "Europe/Lisbon" || !second.GetNow().AsTime().Equal(t0) ||
		len(second.GetTools()) != len(r.hub.cfg.Tools.Specs()) {
		t.Fatalf("step: %v", second)
	}
	if want := []string{"owner", "tool_call", "tool_result"}; !equal(kinds(second.GetEvents()), want) {
		t.Fatalf("second step saw %v", kinds(second.GetEvents()))
	}
}

func TestATurnStopsAfterEightTools(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		return toolCall("list_appointments", `{}`), nil
	})
	r.hub.Owner(context.Background(), "whatsapp", "chat", "go", Source{})
	if got := r.channel.next(t); got != couldNotFinish {
		t.Fatalf("sent %q", got)
	}
	r.settle(t)
	_, events := r.only(t)
	results := 0
	for _, e := range events {
		if e.GetToolResult() != nil {
			results++
		}
	}
	if results != 8 {
		t.Fatalf("%d tools ran: %v", results, kinds(events))
	}
	if steps := len(r.agent.requests()); steps != 9 {
		t.Fatalf("%d steps", steps)
	}
	if last := events[len(events)-1]; last.GetAgent() != couldNotFinish {
		t.Fatalf("last event: %v", last)
	}
}

func TestAnAgentErrorSaysSo(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the reply was not a valid action"))
	})
	r.hub.Owner(context.Background(), "whatsapp", "chat", "go", Source{})
	if got := r.channel.next(t); got != couldNotFinish {
		t.Fatalf("sent %q", got)
	}
}

func TestAnUnreachableAgentSaysSo(t *testing.T) {
	r := newRig(t, openStore(t), nil)
	r.hub.cfg.Agents["codex"] = client(filepath.Join(sockDir(t), "nobody.sock"))
	for _, name := range []string{"codex", "unknown"} { // a dead socket, an agent not configured
		r.settings.set(true, name)
		r.hub.Owner(context.Background(), "whatsapp", "chat", "go", Source{})
		if got, want := r.channel.next(t), couldNotFinish+" "+cantAnswer(name, connect.CodeUnavailable); got != want {
			t.Fatalf("%s: sent %q, want %q", name, got, want)
		}
		r.settle(t)
	}
}

func TestAFailedTurnOnAnErrandsQuestionOrEndRelaysItAsTheSiteSaidIt(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the reply was not a valid action"))
	})
	r.hub.cfg.Agents["codex"] = client(filepath.Join(sockDir(t), "nobody.sock"))
	r.linked(t, 41)
	for _, name := range []string{"claude", "codex"} { // a step error, an unreachable agent
		r.settings.set(true, name)
		r.errands.wait(41, "question", 7)
		r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "question", Text: "Pick a time", Options: []string{"10:00", "11:00"}, Wait: 7})
		if got := r.channel.next(t); got != "Pick a time\n10:00\n11:00" {
			t.Fatalf("%s: sent %q", name, got)
		}
		r.settle(t)
		for _, kind := range []string{"done", "not_done", "failed", "stopped"} {
			r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: kind, Text: "the errand's words: " + kind})
			if got := r.channel.next(t); got != "the errand's words: "+kind {
				t.Fatalf("%s %s: sent %q", name, kind, got)
			}
			r.settle(t)
		}
	}
	// An owner's message is still told the agent couldn't finish.
	r.settings.set(true, "claude")
	r.hub.Owner(context.Background(), "whatsapp", "chat", "go", Source{})
	if got := r.channel.next(t); got != couldNotFinish {
		t.Fatalf("sent %q", got)
	}
}

func TestTurnsOnOneConversationQueue(t *testing.T) {
	var inFlight, most atomic.Int32
	stored := make(chan struct{})
	r := newRig(t, openStore(t), func(n int, req *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		track(&inFlight, &most)
		defer inFlight.Add(-1)
		events := req.GetEvents()
		if events[len(events)-1].GetToolResult() == nil {
			if n == 1 {
				<-stored // "one"'s turn steps again only once "two" is stored
			}
			time.Sleep(20 * time.Millisecond)
			return toolCall("list_appointments", `{}`), nil
		}
		last, _ := lastOwner(req)
		return say("re: " + last), nil
	})
	// Owner returns once the message is stored and queued: "two" comes while "one"'s turn runs, and
	// that turn never sees it.
	r.hub.Owner(context.Background(), "whatsapp", "chat", "one", Source{})
	r.hub.Owner(context.Background(), "whatsapp", "chat", "two", Source{})
	close(stored)
	if a, b := r.channel.next(t), r.channel.next(t); a != "re: one" || b != "re: two" {
		t.Fatalf("answered %q then %q", a, b)
	}
	r.settle(t)
	if most.Load() != 1 {
		t.Fatalf("%d steps at once on one conversation", most.Load())
	}
	_, events := r.only(t)
	want := []string{"owner", "owner", "tool_call", "tool_result", "agent", "tool_call", "tool_result", "agent"}
	if !equal(kinds(events), want) || events[0].GetOwner() != "one" || events[1].GetOwner() != "two" {
		t.Fatalf("events: %v", kinds(events))
	}
	if pending, err := r.store.Pending(context.Background()); err != nil || len(pending) != 0 {
		t.Fatalf("still pending: %v %v", pending, err)
	}
}

func TestTurnsOnOneConversationNeverOverlap(t *testing.T) {
	var inFlight, most atomic.Int32
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		track(&inFlight, &most)
		defer inFlight.Add(-1)
		time.Sleep(30 * time.Millisecond)
		return say("ok"), nil
	})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); r.hub.Owner(context.Background(), "whatsapp", "chat", "hi", Source{}) }()
	}
	wg.Wait()
	r.channel.next(t)
	r.channel.next(t)
	if most.Load() != 1 {
		t.Fatalf("%d steps at once on one conversation", most.Load())
	}
}

func TestQueuedMessagesOutliveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	first, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	stepping, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	r := newRig(t, first, func(n int, _ *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		if n == 1 {
			close(stepping)
		}
		<-release // the engine stops during the first turn's step
		return say("too late"), nil
	})
	r.hub.Owner(context.Background(), "whatsapp", "chat", "one", Source{})
	r.hub.Owner(context.Background(), "whatsapp", "chat", "two", Source{})
	<-stepping
	r.stop()
	r.channel.none(t)
	if _, events := r.only(t); !equal(kinds(events), []string{"owner", "owner"}) {
		t.Fatalf("kept %v", kinds(events))
	}
	first.Close()

	second := openStoreAt(t, path)
	r2 := newRig(t, second, func(_ int, req *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		last, _ := lastOwner(req)
		return say("re: " + last), nil
	})
	if a, b := r2.channel.next(t), r2.channel.next(t); a != "re: one" || b != "re: two" {
		t.Fatalf("after the restart answered %q then %q", a, b)
	}
	r2.settle(t)
	if _, events := r2.only(t); !equal(kinds(events), []string{"owner", "owner", "agent", "agent"}) {
		t.Fatalf("events: %v", kinds(events))
	}
	if pending, err := second.Pending(context.Background()); err != nil || len(pending) != 0 {
		t.Fatalf("still pending: %v %v", pending, err)
	}
}

func TestAnErrandEndingDuringTheStopIsKeptAndToldAfterTheRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	first, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, first, func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		t.Error("stepped during the stop")
		return say("no"), nil
	})
	r.linked(t, 41)
	// The engine stops: the hub's turns end first, then signins cancels its runs, whose end the hub
	// still takes in (pending) before its intake closes.
	r.stop()
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "failed", Text: "the box restarted"})
	r.hub.Close()
	r.channel.none(t)
	if pending, err := first.Pending(context.Background()); err != nil || len(pending) != 1 ||
		pending[0].Event.GetErrand().GetKind() != "failed" {
		t.Fatalf("pending: %v %v", pending, err)
	}
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "done"}) // after the intake closed: dropped
	first.Close()

	second := openStoreAt(t, path)
	r2 := newRig(t, second, func(_ int, req *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		x := req.GetEvents()[len(req.GetEvents())-1].GetErrand()
		return say("The errand " + x.GetKind() + ": " + x.GetText()), nil
	})
	if got := r2.channel.next(t); got != "The errand failed: the box restarted" {
		t.Fatalf("after the restart sent %q", got)
	}
	r2.settle(t)
	r2.channel.none(t)
}

// linked is a conversation of the rig that started run.
func (r *rig) linked(t *testing.T, run int64) int64 {
	t.Helper()
	c, err := r.store.Open(context.Background(), "whatsapp", "chat", t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.store.LinkRun(context.Background(), c.ID, run); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func TestAnErrandsQuestionBecomesATurn(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		return say("Which time?"), nil
	})
	conv := r.linked(t, 41)
	r.errands.wait(41, "question", 7)
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "question", Text: "Pick a time", Options: []string{"10:00", "11:00"}, Wait: 7})
	if got := r.channel.next(t); got != "Which time?" {
		t.Fatalf("sent %q", got)
	}
	steps := r.agent.requests()
	if len(steps) != 1 {
		t.Fatalf("steps: %d", len(steps))
	}
	events := steps[0].GetEvents()
	x := events[len(events)-1].GetErrand()
	if x.GetRun() != 41 || x.GetKind() != "question" || x.GetText() != "Pick a time" || !equal(x.GetOptions(), []string{"10:00", "11:00"}) || x.GetWait() != 7 {
		t.Fatalf("last event: %v", events[len(events)-1])
	}
	r.settle(t)
	if _, stored, _, _ := r.store.One(context.Background(), conv); !equal(kinds(plain(stored)), []string{"errand:question", "agent"}) {
		t.Fatalf("stored: %v", kinds(plain(stored)))
	}
}

// answeringAgent answers the owner's newest message by passing it to run 41's wait, then says "Done".
func answeringAgent(_ int, req *agentv1.StepRequest) (*agentv1.StepResponse, error) {
	events := req.GetEvents()
	if events[len(events)-1].GetToolResult() != nil {
		return say("Done"), nil
	}
	_, id := lastOwner(req)
	return toolCall("answer_errand", fmt.Sprintf(`{"run":41,"answer":"","owner_event":%d}`, id)), nil
}

// lastResult is the newest tool result kept in the conversation.
func (r *rig) lastResult(t *testing.T) string {
	t.Helper()
	_, events := r.only(t)
	for i := len(events) - 1; i >= 0; i-- {
		if res := events[i].GetToolResult(); res != nil {
			return res.GetResultJson()
		}
	}
	t.Fatalf("no tool ran: %v", kinds(events))
	return ""
}

func TestAConfirmIsRelayedAsTheSiteSaidIt(t *testing.T) {
	r := newRig(t, openStore(t), answeringAgent)
	conv := r.linked(t, 41)
	r.errands.wait(41, "confirm", 9)
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "confirm", Text: "Confirm: Fri 21:00 for 4?", Options: []string{"Yes", "No"}, Wait: 9})
	if got := r.channel.next(t); got != "Confirm: Fri 21:00 for 4?\nYes\nNo" {
		t.Fatalf("sent %q", got)
	}
	r.settle(t)
	if steps := r.agent.requests(); len(steps) != 0 {
		t.Fatalf("the agent was stepped for a Confirm: %d", len(steps))
	}
	if _, wait, _, ok, err := r.store.pendingConfirm(context.Background(), conv, 41); err != nil || !ok || wait != 9 {
		t.Fatalf("pending confirm: wait %d %v %v", wait, ok, err)
	}

	// The owner's next message answers it, through the agent's answer_errand.
	r.hub.Owner(context.Background(), "whatsapp", "chat", "yes, do it", Source{})
	if got := r.channel.next(t); got != "Done" {
		t.Fatalf("sent %q", got)
	}
	if got := r.errands.answered(41); !equal(got, []string{"yes, do it"}) {
		t.Fatalf("answered %q, result %s", got, r.lastResult(t))
	}
	if _, events := r.only(t); !equal(kinds(events), []string{"errand:confirm", "agent", "owner", "tool_call", "tool_result", "agent"}) {
		t.Fatalf("events: %v", kinds(events))
	}
}

func TestAConfirmNotSentIsNeverAnswerable(t *testing.T) {
	r := newRig(t, openStore(t), answeringAgent)
	r.linked(t, 41)
	r.errands.wait(41, "confirm", 9)
	r.channel.err = errors.New("whatsappd away")
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "confirm", Text: "Confirm: go?", Wait: 9})
	r.settle(t)
	r.hub.Owner(context.Background(), "whatsapp", "chat", "yes", Source{}) // "try again", say, on something else
	r.settle(t)
	if got := r.errands.answered(41); len(got) != 0 {
		t.Fatalf("an untold Confirm was answered %q", got)
	}
	if res := r.lastResult(t); !strings.Contains(res, "not told") {
		t.Fatalf("answer_errand gave %s", res)
	}
}

func TestAConfirmWithTheChannelOffIsNeverAnswerable(t *testing.T) {
	r := newRig(t, openStore(t), answeringAgent)
	conv := r.linked(t, 41)
	r.errands.wait(41, "confirm", 9)
	r.settings.set(false, "claude")
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "confirm", Text: "Confirm: go?", Wait: 9})
	r.settle(t)
	r.channel.none(t)
	yes := appendEvent(t, r.store, conv, owner("yes"))
	refusedSaying(t, `not told`, call(t, r.hub.cfg.Tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(yes)+`}`))
	if got := r.errands.answered(41); len(got) != 0 {
		t.Fatalf("answered %q", got)
	}
}

func TestAHumanCheckSendsTheConsoleLink(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		t.Error("the agent was stepped for a human check")
		return say("no"), nil
	})
	conv := r.linked(t, 41)
	r.errands.wait(41, "human_check", 0)
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "human_check", Text: "a captcha"})
	if got, want := r.channel.next(t), "The site wants a human check. Open the errand in the console: http://box.local/errands/runs/41"; got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
	r.settle(t)
	if _, stored, _, _ := r.store.One(context.Background(), conv); !equal(kinds(plain(stored)), []string{"errand:human_check", "agent"}) {
		t.Fatalf("stored: %v", kinds(plain(stored)))
	}
	r.hub.cfg.ConsoleURL = ""
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "human_check"})
	if got, want := r.channel.next(t), "The site wants a human check. Open the errand in the console: /errands/runs/41"; got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

func TestAnEventBeforeItsLinkWaitsForIt(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		return say("It's done"), nil
	})
	start := time.Now()
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "done", Text: "made it"})
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Fatalf("RunEvent took %v", took)
	}
	time.Sleep(300 * time.Millisecond) // start_errand's turn links the run only after StartErrand returns
	r.linked(t, 41)
	if got := r.channel.next(t); got != "It's done" {
		t.Fatalf("sent %q", got)
	}
}

func TestAnEventOfNoConversationIsDropped(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		t.Error("stepped for a run no conversation started")
		return say("no"), nil
	})
	r.hub.linkWait = 200 * time.Millisecond
	start := time.Now()
	r.hub.RunEvent(signins.RunEvent{Run: 99, Kind: "question", Wait: 1})
	r.hub.RunEvent(signins.RunEvent{Run: 99, Kind: "question", Wait: 2})
	r.hub.RunEvent(signins.RunEvent{Run: 99, Kind: "done"})
	r.settle(t)
	if took := time.Since(start); took > 500*time.Millisecond { // one wait for the run, not one per event
		t.Fatalf("dropping three events took %v", took)
	}
	r.channel.none(t)
	if list, _ := r.store.List(context.Background(), ""); len(list) != 0 {
		t.Fatalf("conversations: %v", list)
	}
}

func TestNoChosenAgentNoTurn(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		t.Error("stepped with no agent chosen or the channel off")
		return say("no"), nil
	})
	r.settings.set(true)
	r.hub.Owner(context.Background(), "whatsapp", "chat", "hello", Source{})
	r.settle(t)
	r.settings.set(false, "claude")
	r.hub.Owner(context.Background(), "whatsapp", "chat", "again", Source{})
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "human_check"})
	r.linked(t, 41)
	r.settle(t)
	r.channel.none(t)
	if _, events := r.only(t); !equal(kinds(events), []string{"owner", "owner", "errand:human_check"}) {
		t.Fatalf("events: %v", kinds(events))
	}
}

func TestSettingsThatCantBeReadAreRetriedTheEventKeptPending(t *testing.T) {
	r := newRig(t, openStore(t), func(_ int, req *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		if x := req.GetEvents()[len(req.GetEvents())-1].GetErrand(); x != nil {
			return say("asked: " + x.GetText()), nil
		}
		last, _ := lastOwner(req)
		return say("re: " + last), nil
	})
	r.hub.retryFirst, r.hub.retryMax = 20*time.Millisecond, 40*time.Millisecond
	// An owner's message: kept at once, pending while hostd is away, answered once it is back.
	r.settings.fail(3)
	r.hub.Owner(context.Background(), "whatsapp", "chat", "hello", Source{})
	if pending, err := r.store.Pending(context.Background()); err != nil || len(pending) != 1 {
		t.Fatalf("pending: %v %v", pending, err)
	}
	if got := r.channel.next(t); got != "re: hello" {
		t.Fatalf("sent %q", got)
	}
	r.settle(t)
	if n := r.settings.readCount(); n != 4 {
		t.Fatalf("%d reads", n)
	}
	// An errand's question likewise: told once the settings read, never dropped as taken.
	r.linked(t, 41)
	r.errands.wait(41, "question", 7)
	r.settings.fail(4)
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "question", Text: "Pick a time", Wait: 7})
	if got := r.channel.next(t); got != "asked: Pick a time" {
		t.Fatalf("sent %q", got)
	}
	r.settle(t)
	if pending, err := r.store.Pending(context.Background()); err != nil || len(pending) != 0 {
		t.Fatalf("pending: %v %v", pending, err)
	}
}

func TestAStopWhileSettingsCantBeReadLeavesTheEventPending(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) { return say("ok"), nil })
	r.hub.retryFirst, r.hub.retryMax = 10*time.Millisecond, 10*time.Millisecond
	r.settings.fail(1 << 30)
	r.hub.Owner(context.Background(), "whatsapp", "chat", "hello", Source{})
	deadline := time.Now().Add(5 * time.Second)
	for r.settings.readCount() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the settings were read %d times, not retried", r.settings.readCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.stop()
	r.channel.none(t)
	if pending, err := r.store.Pending(context.Background()); err != nil || len(pending) != 1 {
		t.Fatalf("pending: %v %v", pending, err)
	}
}

func TestAnIdleConversationIsNotReused(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		return say("ok"), nil
	})
	r.hub.Owner(context.Background(), "whatsapp", "chat", "first", Source{})
	r.channel.next(t)
	r.settle(t)
	r.clock.set(t0.Add(IdleAfter + time.Minute))
	r.hub.Owner(context.Background(), "whatsapp", "chat", "later", Source{})
	r.channel.next(t)
	r.settle(t)
	list, err := r.store.List(context.Background(), "")
	if err != nil || len(list) != 2 {
		t.Fatalf("conversations: %+v %v", list, err)
	}
	if list[0].State != "open" || list[1].State != "closed" {
		t.Fatalf("states: %+v", list)
	}
	if steps := r.agent.requests(); len(steps[1].GetEvents()) != 1 {
		t.Fatalf("the later turn saw %v", kinds(steps[1].GetEvents()))
	}
}

func TestAnEndClosesTheConversation(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		return &agentv1.StepResponse{Action: &agentv1.StepResponse_End{End: "Bye"}}, nil
	})
	r.hub.Owner(context.Background(), "whatsapp", "chat", "that's all", Source{})
	if got := r.channel.next(t); got != "Bye" {
		t.Fatalf("sent %q", got)
	}
	r.settle(t)
	if c, events := r.only(t); c.State != "closed" || !equal(kinds(events), []string{"owner", "agent"}) {
		t.Fatalf("%+v %v", c, kinds(events))
	}
}

func TestAnEndKeepsAConversationWithARunningErrand(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		return &agentv1.StepResponse{Action: &agentv1.StepResponse_End{End: "I'll tell you"}}, nil
	})
	r.linked(t, 41)
	r.errands.runs[41] = signins.AgentRunDetail{AgentRunView: signins.AgentRunView{Run: signins.Run{ID: 41, State: "running"}}}
	r.hub.Owner(context.Background(), "whatsapp", "chat", "let me know", Source{})
	r.channel.next(t)
	r.settle(t)
	if c, _ := r.only(t); c.State != "open" {
		t.Fatalf("closed with its errand running: %+v", c)
	}
}

func TestTheAgentSeesTheLast60Events(t *testing.T) {
	store := openStore(t)
	conv := open(t, store, "chat")
	for i := range 70 {
		appendEvent(t, store, conv, owner(fmt.Sprint("old ", i)))
	}
	r := newRig(t, store, func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) { return say("ok"), nil })
	r.hub.Owner(context.Background(), "whatsapp", "chat", "now", Source{})
	r.channel.next(t)
	events := r.agent.requests()[0].GetEvents()
	if len(events) != 60 || events[59].GetOwner() != "now" || events[0].GetOwner() != "old 11" {
		t.Fatalf("saw %d events, from %v to %v", len(events), events[0], events[len(events)-1])
	}
}

func TestAConversationOutlivesTheEngine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	step := func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) { return say("hello there"), nil }
	first, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, first, step)
	r.hub.Owner(context.Background(), "whatsapp", "chat", "hello", Source{})
	r.channel.next(t)
	r.settle(t)
	first.Close()

	second := openStoreAt(t, path)
	r2 := newRig(t, second, step)
	r2.hub.Owner(context.Background(), "whatsapp", "chat", "still there?", Source{})
	r2.channel.next(t)
	events := r2.agent.requests()[0].GetEvents()
	if !equal(kinds(events), []string{"owner", "agent", "owner"}) || events[0].GetOwner() != "hello" ||
		events[1].GetAgent() != "hello there" || events[2].GetOwner() != "still there?" {
		t.Fatalf("after the restart the agent saw %v", events)
	}
}

func openStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestASendThatFailsIsStillKept(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) { return say("ok"), nil })
	r.channel.err = errors.New("whatsappd away")
	r.hub.Owner(context.Background(), "whatsapp", "chat", "hi", Source{})
	r.settle(t)
	if _, events := r.only(t); !equal(kinds(events), []string{"owner", "agent"}) {
		t.Fatalf("events: %v", kinds(events))
	}
}

// pendingIn stores the event in the conversation as the hub takes it in, pending, as a stop left it.
func pendingIn(t *testing.T, s *Store, conv int64, e *agentv1.Event) int64 {
	t.Helper()
	id, err := s.Intake(context.Background(), conv, e, t0, Source{})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAWaitThatEndedIsToldNothing(t *testing.T) {
	store := openStore(t)
	conv := open(t, store, "chat")
	if err := store.LinkRun(context.Background(), conv, 41); err != nil {
		t.Fatal(err)
	}
	// Left pending by a stop; the restart ended the run, which waits on nothing now.
	pendingIn(t, store, conv, &agentv1.Event{Kind: &agentv1.Event_Errand{Errand: &agentv1.ErrandEvent{
		Run: 41, Kind: "confirm", Text: "Confirm: go?", Options: []string{"Yes"}, Wait: 9}}})
	pendingIn(t, store, conv, &agentv1.Event{Kind: &agentv1.Event_Errand{Errand: &agentv1.ErrandEvent{
		Run: 41, Kind: "question", Text: "Which?", Wait: 8}}})
	r := newRig(t, store, func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		t.Error("stepped for a wait that ended")
		return say("no"), nil
	})
	r.settle(t)
	// Live: a human check solved, and a question answered in the console, before their turns.
	r.errands.wait(41, "question", 10)
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "human_check"})
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "question", Text: "Which time?", Wait: 7})
	r.settle(t)
	r.channel.none(t)
	if _, _, _, ok, err := store.pendingConfirm(context.Background(), conv, 41); ok || err != nil {
		t.Fatalf("the ended Confirm counts as told: %v %v", ok, err)
	}
	if pending, err := store.Pending(context.Background()); err != nil || len(pending) != 0 {
		t.Fatalf("not taken: %v %v", pending, err)
	}
}

func TestAnOrphanRunIsLookedForOnce(t *testing.T) {
	r := newRig(t, openStore(t), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		t.Error("stepped for a run no conversation started")
		return say("no"), nil
	})
	r.hub.linkWait = 200 * time.Millisecond
	r.hub.RunEvent(signins.RunEvent{Run: 99, Kind: "question", Wait: 1})
	r.settle(t) // its routing goroutine ends: the next event starts another
	start := time.Now()
	r.hub.RunEvent(signins.RunEvent{Run: 99, Kind: "question", Wait: 2})
	r.settle(t)
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("a later event of an orphan run waited %v", took)
	}
	r.hub.RunEvent(signins.RunEvent{Run: 99, Kind: "done"})
	r.settle(t)
	r.hub.mu.Lock()
	left := len(r.hub.orphans)
	r.hub.mu.Unlock()
	if left != 0 {
		t.Fatalf("the ended run is still remembered: %d", left)
	}
}

func TestAResumedTurnSeesItsCutToolCallEnded(t *testing.T) {
	store := openStore(t)
	conv := open(t, store, "chat")
	pendingIn(t, store, conv, owner("start it"))
	appendEvent(t, store, conv, &agentv1.Event{Kind: &agentv1.Event_ToolCall{ToolCall: &agentv1.ToolCall{
		CallId: "c7", Name: "start_errand", ArgumentsJson: `{}`}}}) // the stop came before its result
	appendEvent(t, store, conv, &agentv1.Event{Kind: &agentv1.Event_ToolCall{ToolCall: &agentv1.ToolCall{
		CallId: "c6", Name: "list_accounts", ArgumentsJson: `{}`}}})
	appendEvent(t, store, conv, &agentv1.Event{Kind: &agentv1.Event_ToolResult{ToolResult: &agentv1.ToolResult{
		CallId: "c6", ResultJson: `{"accounts":[]}`}}})
	r := newRig(t, store, func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) { return say("ok"), nil })
	r.channel.next(t)
	events := r.agent.requests()[0].GetEvents()
	if want := []string{"owner", "tool_call", "tool_call", "tool_result", "tool_result"}; !equal(kinds(events), want) {
		t.Fatalf("the resumed turn saw %v", kinds(events))
	}
	if res := events[4].GetToolResult(); res.GetCallId() != "c7" || res.GetResultJson() != restarted {
		t.Fatalf("the cut call's result: %v", res)
	}
}

// failing is a step that fails with code.
func failing(code connect.Code) stepFunc {
	return func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		return nil, connect.NewError(code, errors.New("the daemon's words"))
	}
}

// saying is a step that says text.
func saying(text string) stepFunc {
	return func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) { return say(text), nil }
}

// stored is the one conversation's events as kept, with their agents.
func (r *rig) stored(t *testing.T) []StoredEvent {
	t.Helper()
	c, _ := r.only(t)
	_, events, _, err := r.store.One(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// saidBy is who said each of the conversation's agent events ("" for the hub), by its words.
func (r *rig) saidBy(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, se := range r.stored(t) {
		if a, ok := se.Event.GetKind().(*agentv1.Event_Agent); ok {
			out[a.Agent] = se.Agent
		}
	}
	return out
}

func TestPrimaryAnswers(t *testing.T) {
	r := newRigOf(t, openStore(t), saying("hi"), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		t.Error("the secondary was stepped")
		return say("no"), nil
	})
	r.hub.Owner(context.Background(), "whatsapp", "chat", "hello", Source{})
	if got := r.channel.next(t); got != "hi" {
		t.Fatalf("sent %q", got)
	}
	r.settle(t)
	r.channel.none(t)
	if by := r.saidBy(t); by["hi"] != "claude" {
		t.Fatalf("said by %v", by)
	}
}

func TestFallbackOnEachCode(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodeResourceExhausted, connect.CodeUnavailable} {
		t.Run(code.String(), func(t *testing.T) {
			r := newRigOf(t, openStore(t), failing(code), saying("ok"))
			r.hub.Owner(context.Background(), "whatsapp", "chat", "hello", Source{})
			notice := switchNotice("claude", "codex", code)
			if a, b := r.channel.next(t), r.channel.next(t); a != notice || b != "ok" {
				t.Fatalf("sent %q then %q", a, b)
			}
			r.settle(t)
			r.channel.none(t)
			if by := r.saidBy(t); by["ok"] != "codex" || by[notice] != "" || len(by) != 2 {
				t.Fatalf("said by %v", by)
			}
		})
	}
}

func TestTheNoticeAndTheReasonsWords(t *testing.T) {
	cases := []struct{ got, want string }{
		{switchNotice("claude", "codex", connect.CodeUnauthenticated), "Claude can't answer now (not signed in); Codex is answering."},
		{switchNotice("codex", "claude", connect.CodeResourceExhausted), "Codex can't answer now (out of its plan's limit); Claude is answering."},
		{cantAnswer("claude", connect.CodeUnavailable), "Claude can't answer now (not reachable)."},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%q, want %q", c.got, c.want)
		}
	}
	for code, want := range map[connect.Code]bool{connect.CodeUnauthenticated: true, connect.CodeResourceExhausted: true,
		connect.CodeUnavailable: true, connect.CodeInternal: false, connect.CodeDeadlineExceeded: false, connect.CodeUnknown: false} {
		if got := fallsBack(connect.NewError(code, errors.New("x"))); got != want {
			t.Errorf("fallsBack(%v) = %v", code, got)
		}
	}
	if fallsBack(errors.New("not a step's")) {
		t.Error("a plain error falls back")
	}
}

func TestNoFallbackOnOtherCodes(t *testing.T) {
	r := newRigOf(t, openStore(t), failing(connect.CodeInternal), func(int, *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		t.Error("the secondary was stepped")
		return say("no"), nil
	})
	r.hub.Owner(context.Background(), "whatsapp", "chat", "hello", Source{})
	if got := r.channel.next(t); got != couldNotFinish {
		t.Fatalf("sent %q", got)
	}
	r.settle(t)
	r.channel.none(t)
}

func TestNextTurnTriesThePrimaryAgain(t *testing.T) {
	r := newRigOf(t, openStore(t), func(n int, _ *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		if n == 1 {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
		}
		return say("back"), nil
	}, saying("ok"))
	r.hub.Owner(context.Background(), "whatsapp", "chat", "one", Source{})
	r.channel.next(t)
	r.channel.next(t)
	r.settle(t)
	r.hub.Owner(context.Background(), "whatsapp", "chat", "two", Source{})
	if got := r.channel.next(t); got != "back" {
		t.Fatalf("sent %q", got)
	}
	r.settle(t)
	r.channel.none(t)
	if n := len(r.agent.requests()); n != 2 {
		t.Fatalf("the primary stepped %d times", n)
	}
	if n := len(r.codex.requests()); n != 1 {
		t.Fatalf("the secondary stepped %d times", n)
	}
}

func TestNoticeOncePerStretch(t *testing.T) {
	r := newRigOf(t, openStore(t), func(n int, _ *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		if n == 3 {
			return say("claude here"), nil
		}
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("signed out"))
	}, saying("codex here"))
	notice := switchNotice("claude", "codex", connect.CodeUnauthenticated)
	var sent []string
	for _, text := range []string{"one", "two", "three", "four"} {
		r.hub.Owner(context.Background(), "whatsapp", "chat", text, Source{})
		r.settle(t)
	loop:
		for {
			select {
			case s := <-r.channel.sent:
				sent = append(sent, s)
			default:
				break loop
			}
		}
	}
	want := []string{notice, "codex here", "codex here", "claude here", notice, "codex here"}
	if !equal(sent, want) {
		t.Fatalf("sent %q, want %q", sent, want)
	}
}

func TestPromotedPrimaryGetsNoNotice(t *testing.T) {
	r := newRigOf(t, openStore(t), failing(connect.CodeUnavailable), saying("ok"))
	r.hub.Owner(context.Background(), "whatsapp", "chat", "one", Source{})
	r.channel.next(t) // the notice
	r.channel.next(t)
	r.settle(t)
	r.settings.set(true, "codex") // codex is the primary now, claude removed
	r.hub.Owner(context.Background(), "whatsapp", "chat", "two", Source{})
	if got := r.channel.next(t); got != "ok" {
		t.Fatalf("sent %q", got)
	}
	r.settle(t)
	r.channel.none(t)
}

func TestFallbackKeepsTheWindow(t *testing.T) {
	r := newRigOf(t, openStore(t), func(n int, _ *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		if n == 1 {
			return toolCall("list_accounts", `{}`), nil
		}
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}, saying("ok"))
	r.hub.Owner(context.Background(), "whatsapp", "chat", "go", Source{})
	r.channel.next(t)
	if got := r.channel.next(t); got != "ok" {
		t.Fatalf("sent %q", got)
	}
	r.settle(t)
	steps := r.codex.requests()
	if len(steps) != 1 {
		t.Fatalf("the secondary stepped %d times", len(steps))
	}
	if want := []string{"owner", "tool_call", "tool_result", "agent"}; !equal(kinds(steps[0].GetEvents()), want) {
		t.Fatalf("the secondary saw %v", kinds(steps[0].GetEvents()))
	}
	stored := r.stored(t)
	var by []string
	for _, se := range stored {
		by = append(by, se.Agent)
	}
	if want := []string{"", "claude", "claude", "", "codex"}; !equal(by, want) {
		t.Fatalf("said by %q, want %q (%v)", by, want, kinds(plain(stored)))
	}
}

func TestNoSecondary(t *testing.T) {
	r := newRig(t, openStore(t), failing(connect.CodeResourceExhausted))
	r.hub.Owner(context.Background(), "whatsapp", "chat", "go", Source{})
	if got, want := r.channel.next(t), couldNotFinish+" "+cantAnswer("claude", connect.CodeResourceExhausted); got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
	r.settle(t)
	r.channel.none(t)
}

func TestBothCantAnswer(t *testing.T) {
	r := newRigOf(t, openStore(t), failing(connect.CodeUnauthenticated), failing(connect.CodeUnavailable))
	r.hub.Owner(context.Background(), "whatsapp", "chat", "go", Source{})
	if got, want := r.channel.next(t), switchNotice("claude", "codex", connect.CodeUnauthenticated); got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
	if got, want := r.channel.next(t), couldNotFinish+" "+cantAnswer("codex", connect.CodeUnavailable); got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
	r.settle(t)
	r.channel.none(t)
}

func TestLimitMarkedAndCleared(t *testing.T) {
	r := newRigOf(t, openStore(t), func(n int, _ *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		if n == 1 {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("limit"))
		}
		return say("back"), nil
	}, saying("ok"))
	r.hub.Owner(context.Background(), "whatsapp", "chat", "one", Source{})
	r.channel.next(t)
	r.channel.next(t)
	r.settle(t)
	if limited, err := r.store.Limited(context.Background()); err != nil || len(limited) != 1 || !limited["claude"].Equal(t0) {
		t.Fatalf("limited: %v %v", limited, err)
	}
	r.hub.Owner(context.Background(), "whatsapp", "chat", "two", Source{})
	r.channel.next(t)
	r.settle(t)
	if limited, err := r.store.Limited(context.Background()); err != nil || len(limited) != 0 {
		t.Fatalf("still limited: %v %v", limited, err)
	}
}

func TestUsageGoesToTheAgent(t *testing.T) {
	r := newRigOf(t, openStore(t), saying("hi"), saying("no"))
	r.hub.Owner(context.Background(), "whatsapp", "chat", "hello", Source{})
	r.channel.next(t)
	r.settle(t)
	c, _ := r.only(t)
	if u := c.Total("claude"); u.InTokens != 20 || u.OutTokens != 3 || u.Model != "m" {
		t.Fatalf("claude's usage: %+v", c.Usage)
	}
	if u := c.Total("codex"); u.InTokens != 0 {
		t.Fatalf("codex's usage: %+v", c.Usage)
	}
	if len(c.Usage) != 1 || c.Usage[0].Agent != "claude" {
		t.Fatalf("usage: %+v", c.Usage)
	}
}
