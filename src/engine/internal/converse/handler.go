package converse

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	agentv1 "concierge/engine/internal/gen/agent/v1"
	"concierge/engine/internal/gen/agent/v1/agentv1connect"

	"connectrpc.com/connect"
)

// AgentNames are the two agents, in the order the console lists them.
var AgentNames = []string{"claude", "codex"}

const (
	statusTimeout = 4 * time.Second
	loginTimeout  = 60 * time.Second // starting a login runs the CLI
	// finishTimeout outlasts a device sign-in's own wait (the CLI's 15 min for the owner to enter the
	// code), so the daemon ends it, not this request.
	finishTimeout = 16 * time.Minute
	firstWordsMax = 80
)

// AgentStates is each agent's connector state in hostd (not_added, setting_up or added).
type AgentStates interface {
	AgentStates(ctx context.Context) (map[string]string, error)
}

// NewHandler is the console's API for the owner's agent: the agents' sign-in under /api/v1/agents,
// the conversations under /api/v1/conversations, and the agents' connector check (GET
// /api/v1/agents/{name}/check, which the engine asks in-process: the console's mux does not route it).
// agents holds the clients of the agents whose socket is set; store is nil without -agent-db, and the
// conversations then answer 503; states is hostd's connectors (nil: no state is said).
func NewHandler(store *Store, agents map[string]agentv1connect.AgentServiceClient, states AgentStates) http.Handler {
	return (&handler{store: store, agents: agents, states: states, needsCode: map[string]bool{},
		loginTimeout: loginTimeout, finishTimeout: finishTimeout}).routes()
}

func (h *handler) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/agents", h.list)
	mux.HandleFunc("GET /api/v1/agents/{name}/check", h.check)
	mux.HandleFunc("POST /api/v1/agents/{name}/login", h.login)
	mux.HandleFunc("POST /api/v1/agents/{name}/login/finish", h.finish)
	mux.HandleFunc("POST /api/v1/agents/{name}/logout", h.logout)
	mux.HandleFunc("GET /api/v1/conversations", h.conversations)
	mux.HandleFunc("GET /api/v1/conversations/{id}", h.conversation)
	return mux
}

type handler struct {
	store  *Store
	agents map[string]agentv1connect.AgentServiceClient
	states AgentStates

	// needsCode is, per agent, whether the sign-in it started last takes a code the owner types; an
	// agent with none started takes one.
	mu        sync.Mutex
	needsCode map[string]bool

	loginTimeout, finishTimeout time.Duration
}

// AgentView is one agent as the console sees it. State is its connector's in hostd (not_added,
// setting_up or added; "" when hostd can't say): an agent not added runs no daemon, so it is not
// reachable, and that is no fault.
type AgentView struct {
	Name       string `json:"name"`
	SignedIn   bool   `json:"signed_in"`
	Account    string `json:"account"`
	CLIVersion string `json:"cli_version"`
	Model      string `json:"model"`
	Reachable  bool   `json:"reachable"`
	State      string `json:"state"`
	// LimitedAt is when the agent last said its plan's limit was reached; absent when it never has (or
	// without -agent-db).
	LimitedAt *time.Time `json:"limited_at,omitempty"`
}

func (h *handler) status(ctx context.Context, name string) (*agentv1.StatusResponse, error) {
	c, ok := h.agents[name]
	if !ok {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("no socket"))
	}
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	res, err := c.Status(ctx, connect.NewRequest(&agentv1.StatusRequest{}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	out := make([]AgentView, len(AgentNames))
	done := make(chan struct{})
	for i, name := range AgentNames {
		go func() {
			defer func() { done <- struct{}{} }()
			out[i] = AgentView{Name: name}
			if s, err := h.status(r.Context(), name); err == nil {
				out[i] = AgentView{name, s.GetSignedIn(), s.GetAccount(), s.GetCliVersion(), s.GetModel(), true, "", nil}
			}
		}()
	}
	var states map[string]string
	if h.states != nil {
		ctx, cancel := context.WithTimeout(r.Context(), statusTimeout)
		var err error
		if states, err = h.states.AgentStates(ctx); err != nil {
			slog.Warn("converse: reading the agents' connectors", "err", err)
		}
		cancel()
	}
	for range AgentNames {
		<-done
	}
	var limited map[string]time.Time
	if h.store != nil {
		var err error
		if limited, err = h.store.Limited(r.Context()); err != nil {
			slog.Warn("converse: reading the agents' limits", "err", err)
		}
	}
	for i := range out {
		out[i].State = states[out[i].Name]
		if at, ok := limited[out[i].Name]; ok {
			out[i].LimitedAt = &at
		}
	}
	reply(w, http.StatusOK, out)
}

// check is the agent's setup check: passed once its CLI is signed in.
func (h *handler) check(w http.ResponseWriter, r *http.Request) {
	name, ok := agentName(w, r)
	if !ok {
		return
	}
	s, err := h.status(r.Context(), name)
	if err != nil {
		fail(w, name, "status", err)
		return
	}
	state := "pending"
	if s.GetSignedIn() {
		state = "passed"
	}
	reply(w, http.StatusOK, map[string]string{"state": state})
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	c, name, ok := h.agent(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.loginTimeout)
	defer cancel()
	res, err := c.LoginStart(ctx, connect.NewRequest(&agentv1.LoginStartRequest{}))
	if err != nil {
		fail(w, name, "login", err)
		return
	}
	h.mu.Lock()
	h.needsCode[name] = res.Msg.GetNeedsCode()
	h.mu.Unlock()
	reply(w, http.StatusOK, map[string]any{"url": res.Msg.GetUrl(), "code": res.Msg.GetCode(), "needs_code": res.Msg.GetNeedsCode()})
}

// finish ends the sign-in the agent started: with the owner's code, or, for a sign-in that takes none
// (a device flow, the code entered on the vendor's page), by waiting for the CLI to have the login,
// as long as the CLI itself waits.
func (h *handler) finish(w http.ResponseWriter, r *http.Request) {
	c, name, ok := h.agent(w, r)
	if !ok {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	h.mu.Lock()
	needsCode, started := h.needsCode[name]
	h.mu.Unlock()
	if dec.Decode(&body) != nil || ((needsCode || !started) && strings.TrimSpace(body.Code) == "") {
		reply(w, http.StatusBadRequest, map[string]string{"error": "a code is needed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.finishTimeout)
	defer cancel()
	if _, err := c.LoginFinish(ctx, connect.NewRequest(&agentv1.LoginFinishRequest{Code: strings.TrimSpace(body.Code)})); err != nil {
		fail(w, name, "login finish", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	c, name, ok := h.agent(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.loginTimeout)
	defer cancel()
	if _, err := c.Logout(ctx, connect.NewRequest(&agentv1.LogoutRequest{})); err != nil {
		fail(w, name, "logout", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func agentName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	for _, n := range AgentNames {
		if n == name {
			return name, true
		}
	}
	reply(w, http.StatusNotFound, map[string]string{"error": "no such agent"})
	return "", false
}

// agent is the request's agent's client; an agent with no socket answers 502 like an unreachable one.
func (h *handler) agent(w http.ResponseWriter, r *http.Request) (agentv1connect.AgentServiceClient, string, bool) {
	name, ok := agentName(w, r)
	if !ok {
		return nil, "", false
	}
	c, ok := h.agents[name]
	if !ok {
		reply(w, http.StatusBadGateway, map[string]string{"error": "the " + name + " service is not reachable"})
		return nil, "", false
	}
	return c, name, true
}

// fail answers an agent's error with a plain message. The daemon's own text is not echoed or logged:
// a login's code, URL or account could be in it.
func fail(w http.ResponseWriter, name, op string, err error) {
	switch connect.CodeOf(err) {
	case connect.CodeFailedPrecondition:
		reply(w, http.StatusConflict, map[string]string{"error": "the " + name + " service can't do that now"})
	case connect.CodeInvalidArgument:
		reply(w, http.StatusBadRequest, map[string]string{"error": "the " + name + " service refused that"})
	default:
		slog.Warn("converse: agent call failed", "agent", name, "op", op, "code", connect.CodeOf(err).String())
		reply(w, http.StatusBadGateway, map[string]string{"error": "the " + name + " service is not reachable"})
	}
}

// ConversationView is a conversation as the console shows it.
type ConversationView struct {
	ID         int64     `json:"id"`
	Channel    string    `json:"channel"`
	State      string    `json:"state"`
	Started    time.Time `json:"started"`
	Last       time.Time `json:"last"`
	FirstWords string    `json:"first_words"`
	CostUSD    float64   `json:"cost_usd"`
	CostKnown  bool      `json:"cost_known"`
	InTokens   int64     `json:"in_tokens"`
	OutTokens  int64     `json:"out_tokens"`
	Model      string    `json:"model"`
}

// EventView is one event of a conversation in plain words.
type EventView struct {
	ID   int64     `json:"id"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // owner, agent, tool, result or errand
	Text string    `json:"text"`
	// Agent is the agent whose step the event belongs to; "" for the owner's and the errands' events.
	Agent string `json:"agent"`
}

// view is a conversation with the usage of agent, or of every agent when it is "".
func view(c Conversation, first, agent string) ConversationView {
	t := c.Total(agent)
	return ConversationView{c.ID, c.Channel, c.State, c.Started, c.Last, cut(first, firstWordsMax), t.CostUSD, t.CostKnown,
		t.InTokens, t.OutTokens, t.Model}
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (h *handler) unavailable(w http.ResponseWriter) bool {
	if h.store != nil {
		return false
	}
	reply(w, http.StatusServiceUnavailable, map[string]string{"error": "agent unavailable"})
	return true
}

func (h *handler) conversations(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w) {
		return
	}
	agent := r.URL.Query().Get("agent")
	if agent != "" && !slices.Contains(AgentNames, agent) {
		reply(w, http.StatusBadRequest, map[string]string{"error": "no such agent"})
		return
	}
	list, err := h.store.List(r.Context(), agent)
	var first map[int64]string
	if err == nil {
		first, err = h.store.FirstWords(r.Context())
	}
	if err != nil {
		slog.Error("converse: list conversations", "err", err)
		reply(w, http.StatusInternalServerError, map[string]string{"error": "conversations could not be read"})
		return
	}
	out := make([]ConversationView, len(list))
	for i, c := range list {
		out[i] = view(c, first[c.ID], agent)
	}
	reply(w, http.StatusOK, out)
}

func (h *handler) conversation(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		reply(w, http.StatusNotFound, map[string]string{"error": "no such conversation"})
		return
	}
	c, events, runs, err := h.store.One(r.Context(), id)
	if errors.Is(err, ErrNoConversation) {
		reply(w, http.StatusNotFound, map[string]string{"error": "no such conversation"})
		return
	}
	if err != nil {
		slog.Error("converse: read conversation", "id", id, "err", err)
		reply(w, http.StatusInternalServerError, map[string]string{"error": "the conversation could not be read"})
		return
	}
	first := ""
	evs := make([]EventView, len(events))
	for i, e := range events {
		evs[i] = eventView(e.Event, e.At)
		evs[i].Agent = e.Agent
		if first == "" && evs[i].Kind == "owner" {
			first = evs[i].Text
		}
	}
	usage := []Usage{}
	for _, u := range c.Usage {
		if u.Agent != "" {
			usage = append(usage, u)
		}
	}
	reply(w, http.StatusOK, struct {
		ConversationView
		Events []EventView `json:"events"`
		Runs   []int64     `json:"runs"`
		Usage  []Usage     `json:"usage"`
	}{view(c, first, ""), evs, runs, usage})
}

// eventView puts an event in words: a tool call as its name and arguments, a result as its JSON, an
// errand's event as its kind and text with the options it offered.
func eventView(e *agentv1.Event, at time.Time) EventView {
	v := EventView{ID: e.GetId(), At: at}
	switch k := e.GetKind().(type) {
	case *agentv1.Event_Owner:
		v.Kind, v.Text = "owner", k.Owner
	case *agentv1.Event_Agent:
		v.Kind, v.Text = "agent", k.Agent
	case *agentv1.Event_ToolCall:
		v.Kind, v.Text = "tool", k.ToolCall.GetName()+"("+k.ToolCall.GetArgumentsJson()+")"
	case *agentv1.Event_ToolResult:
		v.Kind, v.Text = "result", k.ToolResult.GetResultJson()
	case *agentv1.Event_Errand:
		x := k.Errand
		v.Kind, v.Text = "errand", x.GetKind()+": "+x.GetText()
		if len(x.GetOptions()) > 0 {
			v.Text += " (options: " + strings.Join(x.GetOptions(), ", ") + ")"
		}
	}
	return v
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Warn("converse: response not written", "err", err)
	}
}
