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
	want := []AgentView{{"claude", true, "me@example.org", "1.2.3", "m1", true, "added"}, {Name: "codex", State: "not_added"}}
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
