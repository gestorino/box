package converse

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "concierge/engine/internal/gen/agent/v1"
	"concierge/engine/internal/gen/agent/v1/agentv1connect"
	"concierge/engine/internal/h2c"

	"connectrpc.com/connect"
)

// loginAgent is an agent daemon's sign-in side: its status, and what its login calls saw.
type loginAgent struct {
	agentv1connect.UnimplementedAgentServiceHandler
	mu        sync.Mutex
	signedIn  bool
	finished  []string
	loggedOut bool
	startErr  error
	// deviceFlow is a login with no code to type (Codex's): its finish waits for the CLI, which takes
	// finishTakes.
	deviceFlow  bool
	finishTakes time.Duration
}

func (a *loginAgent) Status(context.Context, *connect.Request[agentv1.StatusRequest]) (*connect.Response[agentv1.StatusResponse], error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return connect.NewResponse(&agentv1.StatusResponse{SignedIn: a.signedIn, Account: "me@example.org", CliVersion: "1.2.3", Model: "m1"}), nil
}

func (a *loginAgent) LoginStart(context.Context, *connect.Request[agentv1.LoginStartRequest]) (*connect.Response[agentv1.LoginStartResponse], error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.startErr != nil {
		return nil, a.startErr
	}
	return connect.NewResponse(&agentv1.LoginStartResponse{Url: "https://login.example/x?state=secret", Code: "AB-12", NeedsCode: !a.deviceFlow}), nil
}

func (a *loginAgent) LoginFinish(ctx context.Context, r *connect.Request[agentv1.LoginFinishRequest]) (*connect.Response[agentv1.LoginFinishResponse], error) {
	a.mu.Lock()
	takes := a.finishTakes
	a.mu.Unlock()
	select {
	case <-time.After(takes):
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.finished = append(a.finished, r.Msg.GetCode())
	return connect.NewResponse(&agentv1.LoginFinishResponse{}), nil
}

func (a *loginAgent) Logout(context.Context, *connect.Request[agentv1.LogoutRequest]) (*connect.Response[agentv1.LogoutResponse], error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loggedOut = true
	return connect.NewResponse(&agentv1.LogoutResponse{}), nil
}

func serveLogin(t *testing.T, a *loginAgent) agentv1connect.AgentServiceClient {
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

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

// fakeStates is hostd's connectors, as the agents list reads them.
type fakeStates struct {
	states map[string]string
	err    error
}

func (f fakeStates) AgentStates(context.Context) (map[string]string, error) { return f.states, f.err }

func TestAgentsListReportsEach(t *testing.T) {
	a := &loginAgent{signedIn: true}
	h := NewHandler(nil, map[string]agentv1connect.AgentServiceClient{"claude": serveLogin(t, a)},
		fakeStates{states: map[string]string{"claude": "added", "codex": "not_added"}})
	rec := do(h, "GET", "/api/v1/agents", "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got []AgentView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []AgentView{{"claude", true, "me@example.org", "1.2.3", "m1", true, "added", nil}, {Name: "codex", State: "not_added"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if !strings.Contains(rec.Body.String(), `"reachable":false`) || !strings.Contains(rec.Body.String(), `"state":"not_added"`) {
		t.Fatalf("body %s", rec.Body)
	}
}

func TestAgentsListWithHostdAwayStillAnswers(t *testing.T) {
	a := &loginAgent{signedIn: true}
	h := NewHandler(nil, map[string]agentv1connect.AgentServiceClient{"claude": serveLogin(t, a)},
		fakeStates{err: context.DeadlineExceeded})
	rec := do(h, "GET", "/api/v1/agents", "")
	var got []AgentView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, err)
	}
	if got[0].State != "" || !got[0].Reachable || got[1].State != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestADeviceSignInFinishesWithNoCodeAndOutlivesTheLoginTimeout(t *testing.T) {
	codex := &loginAgent{deviceFlow: true, finishTakes: 300 * time.Millisecond} // the owner enters the code meanwhile
	claude := &loginAgent{}
	h := (&handler{agents: map[string]agentv1connect.AgentServiceClient{"claude": serveLogin(t, claude), "codex": serveLogin(t, codex)},
		needsCode: map[string]bool{}, loginTimeout: 50 * time.Millisecond, finishTimeout: 5 * time.Second}).routes()
	if rec := do(h, "POST", "/api/v1/agents/codex/login", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needs_code":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/agents/codex/login/finish", `{"code":""}`); rec.Code != http.StatusNoContent {
		t.Fatalf("a device sign-in's finish: %d %s", rec.Code, rec.Body)
	}
	if len(codex.finished) != 1 || codex.finished[0] != "" {
		t.Fatalf("finished %q", codex.finished)
	}
	// Claude's sign-in takes the owner's code: none is refused before it reaches the daemon.
	if rec := do(h, "POST", "/api/v1/agents/claude/login", ""); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/agents/claude/login/finish", `{"code":" "}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "a code is needed") || len(claude.finished) != 0 {
		t.Fatalf("%d %s %q", rec.Code, rec.Body, claude.finished)
	}
}

func TestAFinishWithNoSignInStartedNeedsACode(t *testing.T) {
	codex := &loginAgent{deviceFlow: true}
	h := NewHandler(nil, map[string]agentv1connect.AgentServiceClient{"codex": serveLogin(t, codex)}, nil)
	if rec := do(h, "POST", "/api/v1/agents/codex/login/finish", `{"code":""}`); rec.Code != http.StatusBadRequest || len(codex.finished) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestTheFinishWaitsAsLongAsTheCLIsOwnSignIn(t *testing.T) {
	if finishTimeout <= 15*time.Minute {
		t.Fatalf("the finish gives up at %v, before the CLI's 15 min", finishTimeout)
	}
}

func TestAgentCheckIsPassedOnlyWhenSignedIn(t *testing.T) {
	a := &loginAgent{}
	h := NewHandler(nil, map[string]agentv1connect.AgentServiceClient{"claude": serveLogin(t, a)}, nil)
	if rec := do(h, "GET", "/api/v1/agents/claude/check", ""); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"state":"pending"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	a.mu.Lock()
	a.signedIn = true
	a.mu.Unlock()
	if rec := do(h, "GET", "/api/v1/agents/claude/check", ""); strings.TrimSpace(rec.Body.String()) != `{"state":"passed"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/v1/agents/codex/check", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("an unreachable agent: %d %s", rec.Code, rec.Body)
	}
}

func TestLoginProxiesStartAndFinish(t *testing.T) {
	a := &loginAgent{}
	h := NewHandler(nil, map[string]agentv1connect.AgentServiceClient{"claude": serveLogin(t, a)}, nil)
	rec := do(h, "POST", "/api/v1/agents/claude/login", "")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"code":"AB-12","needs_code":true,"url":"https://login.example/x?state=secret"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/agents/claude/login/finish", `{"code":" 123 "}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(a.finished) != 1 || a.finished[0] != "123" {
		t.Fatalf("finished %q", a.finished)
	}
	if rec := do(h, "POST", "/api/v1/agents/claude/login/finish", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no code: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/v1/agents/claude/logout", ""); rec.Code != http.StatusNoContent || !a.loggedOut {
		t.Fatalf("%d", rec.Code)
	}
	if rec := do(h, "POST", "/api/v1/agents/codex/login", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("no socket: %d", rec.Code)
	}
}

func TestLoginErrorsAreMappedAndPlain(t *testing.T) {
	a := &loginAgent{startErr: connect.NewError(connect.CodeFailedPrecondition, context.DeadlineExceeded)}
	h := NewHandler(nil, map[string]agentv1connect.AgentServiceClient{"claude": serveLogin(t, a)}, nil)
	if rec := do(h, "POST", "/api/v1/agents/claude/login", ""); rec.Code != http.StatusConflict {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	a.mu.Lock()
	a.startErr = connect.NewError(connect.CodeUnavailable, context.Canceled)
	a.mu.Unlock()
	if rec := do(h, "POST", "/api/v1/agents/claude/login", ""); rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "cancel") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestUnknownAgentIs404(t *testing.T) {
	h := NewHandler(nil, nil, nil)
	for _, p := range []string{"login", "login/finish", "logout"} {
		if rec := do(h, "POST", "/api/v1/agents/gemini/"+p, `{"code":"1"}`); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	if rec := do(h, "GET", "/api/v1/agents/gemini/check", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("check: %d", rec.Code)
	}
}

func TestConversationListAndOne(t *testing.T) {
	s := openStore(t)
	old := open(t, s, "old")
	appendEvent(t, s, old, owner("first of the old"))
	cv := open(t, s, "new")
	long := strings.Repeat("é", 100)
	appendEvent(t, s, cv, said("an agent word before"))
	appendEvent(t, s, cv, owner(long))
	appendEvent(t, s, cv, &agentv1.Event{Kind: &agentv1.Event_ToolCall{ToolCall: &agentv1.ToolCall{CallId: "c", Name: "start_errand", ArgumentsJson: `{"a":1}`}}})
	appendEvent(t, s, cv, &agentv1.Event{Kind: &agentv1.Event_ToolResult{ToolResult: &agentv1.ToolResult{CallId: "c", ResultJson: `{"run":7}`}}})
	appendEvent(t, s, cv, errandEvent(7, "question", "Which?", "a", "b"))
	appendEvent(t, s, cv, errandEvent(7, "done", "ok"))
	if _, err := s.Append(context.Background(), cv, "", said("later"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkRun(context.Background(), cv, 7); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(context.Background(), cv, "", &agentv1.Usage{Model: "m", CostUsd: 0.5, CostKnown: true, InputTokens: 10, OutputTokens: 3}); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, nil, nil)

	rec := do(h, "GET", "/api/v1/conversations", "")
	var list []ConversationView
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, err)
	}
	if len(list) != 2 || list[0].ID != cv || list[1].FirstWords != "first of the old" {
		t.Fatalf("list %+v", list)
	}
	if list[0].FirstWords != strings.Repeat("é", 80) || list[0].Model != "m" || !list[0].CostKnown || list[0].CostUSD != 0.5 ||
		list[0].InTokens != 10 || list[0].OutTokens != 3 || list[0].Channel != "whatsapp" || list[0].State != "open" {
		t.Fatalf("view %+v", list[0])
	}

	rec = do(h, "GET", "/api/v1/conversations/"+strconv.FormatInt(cv, 10), "")
	var one struct {
		ConversationView
		Events []EventView `json:"events"`
		Runs   []int64     `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, err)
	}
	type kt struct{ k, t string }
	want := []kt{{"agent", "an agent word before"}, {"owner", long}, {"tool", `start_errand({"a":1})`}, {"result", `{"run":7}`},
		{"errand", "question: Which? (options: a, b)"}, {"errand", "done: ok"}, {"agent", "later"}}
	if len(one.Events) != len(want) {
		t.Fatalf("events %+v", one.Events)
	}
	for i, w := range want {
		if one.Events[i].Kind != w.k || one.Events[i].Text != w.t || one.Events[i].ID == 0 {
			t.Fatalf("event %d: %+v, want %+v", i, one.Events[i], w)
		}
	}
	if !one.Events[6].At.Equal(t0.Add(time.Hour)) || !one.Events[0].At.Equal(t0) {
		t.Fatalf("times %v %v", one.Events[0].At, one.Events[6].At)
	}
	if len(one.Runs) != 1 || one.Runs[0] != 7 || one.FirstWords != strings.Repeat("é", 80) {
		t.Fatalf("one %+v", one)
	}

	for _, p := range []string{"999", "x"} {
		if rec := do(h, "GET", "/api/v1/conversations/"+p, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
}

func TestConversationsWithNoHubAre503(t *testing.T) {
	h := NewHandler(nil, nil, nil)
	for _, p := range []string{"/api/v1/conversations", "/api/v1/conversations/1"} {
		rec := do(h, "GET", p, "")
		if rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != `{"error":"agent unavailable"}` {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

// twoAgents is a store with a conversation claude spoke in, one codex spoke in, and one both did.
func twoAgents(t *testing.T) (s *Store, onlyClaude, onlyCodex, both int64) {
	t.Helper()
	ctx := context.Background()
	s = openStore(t)
	onlyClaude, onlyCodex, both = open(t, s, "a"), open(t, s, "b"), open(t, s, "c")
	for _, x := range []struct {
		conv  int64
		agent string
		cost  float64
		in    int64
	}{{onlyClaude, "claude", 1, 10}, {onlyCodex, "codex", 2, 20}, {both, "claude", 4, 40}, {both, "codex", 8, 80}} {
		if _, err := s.Append(ctx, x.conv, x.agent, said("hi from "+x.agent), t0); err != nil {
			t.Fatal(err)
		}
		if err := s.AddUsage(ctx, x.conv, x.agent, &agentv1.Usage{Model: "m-" + x.agent, CostUsd: x.cost, CostKnown: true, InputTokens: x.in, OutputTokens: 1}); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestConversationsByAgent(t *testing.T) {
	s, onlyClaude, _, both := twoAgents(t)
	h := NewHandler(s, nil, nil)
	var list []ConversationView
	rec := do(h, "GET", "/api/v1/conversations?agent=claude", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, err)
	}
	if len(list) != 2 || list[0].ID != both || list[1].ID != onlyClaude {
		t.Fatalf("list %+v", list)
	}
	if list[0].CostUSD != 4 || list[0].InTokens != 40 || list[0].Model != "m-claude" || list[1].CostUSD != 1 {
		t.Fatalf("usage %+v", list)
	}
	rec = do(h, "GET", "/api/v1/conversations", "")
	list = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 3 || list[0].CostUSD != 12 {
		t.Fatalf("all %s %v", rec.Body, err)
	}
}

func TestConversationsUnknownAgent400(t *testing.T) {
	s, _, _, _ := twoAgents(t)
	rec := do(NewHandler(s, nil, nil), "GET", "/api/v1/conversations?agent=gpt", "")
	if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != `{"error":"no such agent"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestConversationEventsCarryAgent(t *testing.T) {
	s, _, _, both := twoAgents(t)
	rec := do(NewHandler(s, nil, nil), "GET", "/api/v1/conversations/"+strconv.FormatInt(both, 10), "")
	var one struct {
		Events []EventView `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || len(one.Events) != 2 {
		t.Fatalf("%s %v", rec.Body, err)
	}
	if one.Events[0].Agent != "claude" || one.Events[1].Agent != "codex" {
		t.Fatalf("events %+v", one.Events)
	}
}

func TestConversationUsagePerAgent(t *testing.T) {
	s, _, _, both := twoAgents(t)
	if err := s.AddUsage(context.Background(), both, "", &agentv1.Usage{Model: "x", CostUsd: 100, CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	rec := do(NewHandler(s, nil, nil), "GET", "/api/v1/conversations/"+strconv.FormatInt(both, 10), "")
	var one struct {
		ConversationView
		Usage []Usage `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if len(one.Usage) != 2 || one.Usage[0].Agent != "claude" || one.Usage[0].CostUSD != 4 || one.Usage[1].Agent != "codex" ||
		one.Usage[1].InTokens != 80 || one.Usage[1].Model != "m-codex" {
		t.Fatalf("usage %+v", one.Usage)
	}
	if one.CostUSD != 112 {
		t.Fatalf("total %v", one.CostUSD)
	}
}

// limitsHandler is the console's API on the store s and the agents, its clock at now.
func limitsHandler(s *Store, agents map[string]agentv1connect.AgentServiceClient, now time.Time) http.Handler {
	return (&handler{store: s, agents: agents, needsCode: map[string]bool{}, loginTimeout: loginTimeout,
		finishTimeout: finishTimeout, now: func() time.Time { return now }}).routes()
}

func TestAgentsReportALimitOfTheLastDayOnly(t *testing.T) {
	s := openStore(t)
	now := t0.Add(48 * time.Hour)
	if err := s.MarkLimited(context.Background(), "claude", now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLimited(context.Background(), "codex", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	rec := do(limitsHandler(s, nil, now), "GET", "/api/v1/agents", "")
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || len(raw) != 2 {
		t.Fatalf("%s %v", rec.Body, err)
	}
	if _, ok := raw[0]["limited_at"]; ok {
		t.Fatalf("claude's limit of 25 h ago reported: %v", raw[0])
	}
	if at, _ := raw[1]["limited_at"].(string); at != now.Add(-time.Hour).Format(time.RFC3339) {
		t.Fatalf("codex: %v", raw[1])
	}
	if limited, err := s.Limited(context.Background()); err != nil || len(limited) != 2 {
		t.Fatalf("the old limit is forgotten: %v %v", limited, err)
	}
}

func TestASignInFinishedOrALogoutClearsTheLimit(t *testing.T) {
	for _, path := range []string{"login/finish", "logout"} {
		t.Run(path, func(t *testing.T) {
			s := openStore(t)
			for _, name := range AgentNames {
				if err := s.MarkLimited(context.Background(), name, t0); err != nil {
					t.Fatal(err)
				}
			}
			h := limitsHandler(s, map[string]agentv1connect.AgentServiceClient{"claude": serveLogin(t, &loginAgent{})}, t0)
			if rec := do(h, "POST", "/api/v1/agents/claude/"+path, `{"code":"123"}`); rec.Code != http.StatusNoContent {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			limited, err := s.Limited(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := limited["claude"]; ok || len(limited) != 1 {
				t.Fatalf("limited after %s: %v", path, limited)
			}
		})
	}
}

func TestAFailedSignInKeepsTheLimit(t *testing.T) {
	s := openStore(t)
	if err := s.MarkLimited(context.Background(), "claude", t0); err != nil {
		t.Fatal(err)
	}
	h := limitsHandler(s, map[string]agentv1connect.AgentServiceClient{"claude": serveLogin(t, &loginAgent{})}, t0)
	if rec := do(h, "POST", "/api/v1/agents/claude/login/finish", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if limited, err := s.Limited(context.Background()); err != nil || len(limited) != 1 {
		t.Fatalf("limited: %v %v", limited, err)
	}
}
