package converse

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"concierge/engine/internal/dbmigrate"
	agentv1 "concierge/engine/internal/gen/agent/v1"

	"google.golang.org/protobuf/proto"
)

var t0 = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func owner(text string) *agentv1.Event {
	return &agentv1.Event{Kind: &agentv1.Event_Owner{Owner: text}}
}

func said(text string) *agentv1.Event {
	return &agentv1.Event{Kind: &agentv1.Event_Agent{Agent: text}}
}

func errandEvent(run int64, kind, text string, options ...string) *agentv1.Event {
	return &agentv1.Event{Kind: &agentv1.Event_Errand{Errand: &agentv1.ErrandEvent{Run: run, Kind: kind, Text: text, Options: options}}}
}

func open(t *testing.T, s *Store, thread string) int64 {
	t.Helper()
	c, err := s.Open(context.Background(), "whatsapp", thread, t0)
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func appendEvent(t *testing.T, s *Store, conv int64, e *agentv1.Event) int64 {
	t.Helper()
	id, err := s.Append(context.Background(), conv, "", e, t0)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAConnectorsMessageIsTakenInOnce(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	conv := open(t, s, "chat")
	if _, ok, err := s.LastHeard(ctx, "whatsapp"); ok || err != nil {
		t.Fatalf("heard before anything: %v %v", ok, err)
	}
	later := Source{Channel: "whatsapp", ID: "351912345678@s.whatsapp.net/B", StoredAt: t0.Add(time.Minute + 500*time.Millisecond)}
	if _, err := s.Intake(ctx, conv, owner("later"), t0, later); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Intake(ctx, conv, owner("later, again"), t0, later); err != ErrHeard {
		t.Fatalf("a replayed message: %v", err)
	}
	if heard, err := s.Heard(ctx, later); !heard || err != nil {
		t.Fatalf("heard: %v %v", heard, err)
	}
	// An older message heard after it (a replay's order) never moves the mark back.
	if _, err := s.Intake(ctx, conv, owner("earlier"), t0, Source{Channel: "whatsapp", ID: "351912345678@s.whatsapp.net/A", StoredAt: t0.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if at, ok, err := s.LastHeard(ctx, "whatsapp"); !ok || err != nil || !at.Equal(later.StoredAt) {
		t.Fatalf("last heard %v %v %v, want %v", at, ok, err, later.StoredAt)
	}
	// Events from no connector's message are never one.
	for range 2 {
		if _, err := s.Intake(ctx, conv, errandEvent(41, "done", "made it"), t0, Source{}); err != nil {
			t.Fatal(err)
		}
	}
	if events, _ := s.Events(ctx, conv, 0); len(events) != 4 {
		t.Fatalf("%d events", len(events))
	}
}

func TestOpenReusesTheOpenConversation(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	a, err := s.Open(ctx, "whatsapp", "chat", t0)
	if err != nil {
		t.Fatal(err)
	}
	if a.Channel != "whatsapp" || a.Thread != "chat" || a.State != "open" || !a.Started.Equal(t0) || !a.Last.Equal(t0) {
		t.Fatalf("opened %+v", a)
	}
	again, err := s.Open(ctx, "whatsapp", "chat", t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != a.ID {
		t.Fatalf("a second conversation %d beside %d", again.ID, a.ID)
	}
	other, err := s.Open(ctx, "email", "chat", t0)
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == a.ID {
		t.Fatal("another channel's thread reused the conversation")
	}

	if err := s.CloseIdle(ctx, t0.Add(3*time.Hour), func(int64) bool { return false }); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Open(ctx, "whatsapp", "chat", t0.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == a.ID || fresh.State != "open" {
		t.Fatalf("after closing, %+v", fresh)
	}
	closed, _, _, err := s.One(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.State != "closed" {
		t.Fatalf("the idle conversation is %q", closed.State)
	}
}

func TestAppendNumbersEvents(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	conv := open(t, s, "chat")
	events := []*agentv1.Event{
		owner("hello"),
		{Kind: &agentv1.Event_ToolCall{ToolCall: &agentv1.ToolCall{CallId: "c1", Name: "list_accounts", ArgumentsJson: "{}"}}},
		{Kind: &agentv1.Event_ToolResult{ToolResult: &agentv1.ToolResult{CallId: "c1", ResultJson: `{"accounts":[]}`}}},
		errandEvent(4, "question", "Which day?", "Fri", "Sat"),
		said("hi"),
	}
	var ids []int64
	for i, e := range events {
		id, err := s.Append(ctx, conv, "", e, t0.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if e.GetId() != id {
			t.Fatalf("event %d: id %d, returned %d", i, e.GetId(), id)
		}
		if i > 0 && id <= ids[i-1] {
			t.Fatalf("ids %v then %d", ids, id)
		}
		ids = append(ids, id)
	}
	last, err := s.Events(ctx, conv, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 3 {
		t.Fatalf("%d events", len(last))
	}
	for i, e := range last {
		if !proto.Equal(e, events[2+i]) {
			t.Fatalf("event %d: %v, want %v", i, e, events[2+i])
		}
	}
	c, _, _, err := s.One(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Last.Equal(t0.Add(4 * time.Minute)) {
		t.Fatalf("last %v", c.Last)
	}
	if _, err := s.Append(ctx, conv+100, "", owner("lost"), t0); err == nil {
		t.Fatal("an event was kept for no conversation")
	}
}

func TestCloseIdleKeepsOneWithARunningErrand(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	busy, idle := open(t, s, "busy"), open(t, s, "idle")
	if err := s.LinkRun(ctx, busy, 7); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkRun(ctx, idle, 8); err != nil {
		t.Fatal(err)
	}
	running := map[int64]bool{7: true}
	isRunning := func(run int64) bool { return running[run] }
	state := func(id int64) string {
		c, _, _, err := s.One(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return c.State
	}

	if err := s.CloseIdle(ctx, t0.Add(2*time.Hour-time.Second), isRunning); err != nil {
		t.Fatal(err)
	}
	if state(busy) != "open" || state(idle) != "open" {
		t.Fatal("closed before two hours")
	}
	if err := s.CloseIdle(ctx, t0.Add(2*time.Hour), isRunning); err != nil {
		t.Fatal(err)
	}
	if state(busy) != "open" || state(idle) != "closed" {
		t.Fatalf("after two hours: busy %s, idle %s", state(busy), state(idle))
	}
	running[7] = false
	if err := s.CloseIdle(ctx, t0.Add(2*time.Hour), isRunning); err != nil {
		t.Fatal(err)
	}
	if state(busy) != "closed" {
		t.Fatal("kept open with its errand over")
	}
}

func TestCloseIdleKeepsOneThatHeardMeanwhile(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	conv := open(t, s, "chat")
	if err := s.LinkRun(ctx, conv, 7); err != nil {
		t.Fatal(err)
	}
	later := t0.Add(2*time.Hour + time.Minute)
	// An event arrives between CloseIdle's look and its close.
	err := s.CloseIdle(ctx, later, func(int64) bool {
		if _, err := s.Append(ctx, conv, "", owner("still here"), later); err != nil {
			t.Error(err)
		}
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, _, _, _ := s.One(ctx, conv); c.State != "open" {
		t.Fatal("closed with an event just in")
	}
}

func TestUsageAddsUp(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	conv := open(t, s, "chat")
	if err := s.AddUsage(ctx, conv, "", &agentv1.Usage{Model: "m1", InputTokens: 100, OutputTokens: 10, CostUsd: 0.25, CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, conv, "", &agentv1.Usage{Model: "m2", InputTokens: 50, OutputTokens: 5, CostUsd: 0.5, CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	c, _, _, err := s.One(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	if u := c.Total(""); u.InTokens != 150 || u.OutTokens != 15 || u.CostUSD != 0.75 || !u.CostKnown || u.Model != "m2" {
		t.Fatalf("two usages: %+v", c)
	}
	if err := s.AddUsage(ctx, conv, "", &agentv1.Usage{Model: "m2", InputTokens: 1, OutputTokens: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, conv, "", &agentv1.Usage{Model: "m2", InputTokens: 1, OutputTokens: 1, CostUsd: 1, CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	if c, _, _, _ = s.One(ctx, conv); c.Total("").CostKnown || c.Total("").InTokens != 152 {
		t.Fatalf("an unknown cost: %+v", c)
	}
}

func TestOneAndListAndTheRunsOfAConversation(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	first, second := open(t, s, "a"), open(t, s, "b")
	appendEvent(t, s, first, owner("hello"))
	if _, err := s.Append(ctx, second, "", owner("later"), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, run := range []int64{3, 5} {
		if err := s.LinkRun(ctx, first, run); err != nil {
			t.Fatal(err)
		}
	}
	c, events, runs, err := s.One(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != first || len(events) != 1 || events[0].Event.GetOwner() != "hello" || len(runs) != 2 || runs[0] != 3 || runs[1] != 5 {
		t.Fatalf("one: %+v %v %v", c, events, runs)
	}
	if conv, ok, err := s.ConversationOfRun(ctx, 5); err != nil || !ok || conv != first {
		t.Fatalf("run 5: %d %v %v", conv, ok, err)
	}
	if _, ok, err := s.ConversationOfRun(ctx, 6); err != nil || ok {
		t.Fatalf("run 6: %v %v", ok, err)
	}
	list, err := s.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != second || list[1].ID != first {
		t.Fatalf("list, newest first: %+v", list)
	}
	if _, _, _, err := s.One(ctx, second+10); err != ErrNoConversation {
		t.Fatalf("no conversation: %v", err)
	}
}

func TestEventsCarryTheirAgent(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	conv := open(t, s, "chat")
	for _, a := range []struct {
		agent string
		e     *agentv1.Event
	}{{"", owner("hi")}, {"claude", said("one")}, {"codex", said("two")}} {
		if _, err := s.Append(ctx, conv, a.agent, a.e, t0); err != nil {
			t.Fatal(err)
		}
	}
	_, events, _, err := s.One(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, e := range events {
		got = append(got, e.Agent)
	}
	if !equal(got, []string{"", "claude", "codex"}) {
		t.Fatalf("agents %q", got)
	}
}

func TestListByAgent(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	a, b, c := open(t, s, "a"), open(t, s, "b"), open(t, s, "c")
	for conv, agents := range map[int64][]string{a: {"claude"}, b: {"codex"}, c: {"claude", "codex"}} {
		for _, agent := range agents {
			if _, err := s.Append(ctx, conv, agent, said("x"), t0.Add(time.Duration(conv)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
	ids := func(agent string) []int64 {
		list, err := s.List(ctx, agent)
		if err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, c := range list {
			out = append(out, c.ID)
		}
		return out
	}
	if got := ids("claude"); len(got) != 2 || got[0] != c || got[1] != a {
		t.Fatalf("claude: %v", got)
	}
	if got := ids("codex"); len(got) != 2 || got[0] != c || got[1] != b {
		t.Fatalf("codex: %v", got)
	}
	if got := ids(""); len(got) != 3 {
		t.Fatalf("all: %v", got)
	}
}

func TestUsagePerAgent(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	conv := open(t, s, "chat")
	if err := s.AddUsage(ctx, conv, "claude", &agentv1.Usage{Model: "c", InputTokens: 10, OutputTokens: 5, CostUsd: 0.01, CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, conv, "codex", &agentv1.Usage{Model: "x", InputTokens: 3, OutputTokens: 1}); err != nil {
		t.Fatal(err)
	}
	c, _, _, err := s.One(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	if u := c.Total("claude"); u.CostUSD != 0.01 || u.InTokens != 10 || u.OutTokens != 5 || !u.CostKnown || u.Model != "c" {
		t.Fatalf("claude: %+v", u)
	}
	if u := c.Total(""); u.CostUSD != 0.01 || u.InTokens != 13 || u.OutTokens != 6 || u.CostKnown {
		t.Fatalf("total: %+v", u)
	}
	if len(c.Usage) != 2 || c.Usage[0].Agent != "claude" || c.Usage[1].Agent != "codex" {
		t.Fatalf("usage rows: %+v", c.Usage)
	}
	if err := s.AddUsage(ctx, conv+100, "claude", &agentv1.Usage{}); err != ErrNoConversation {
		t.Fatalf("no conversation: %v", err)
	}
}

func TestLastAgent(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	conv := open(t, s, "chat")
	last := func() string {
		a, err := s.LastAgent(ctx, conv)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	appendEvent(t, s, conv, owner("hi"))
	if a := last(); a != "" {
		t.Fatalf("owner only: %q", a)
	}
	if _, err := s.Append(ctx, conv, "claude", said("x"), t0); err != nil {
		t.Fatal(err)
	}
	appendEvent(t, s, conv, owner("again"))
	if a := last(); a != "claude" {
		t.Fatalf("after claude: %q", a)
	}
}

func TestLimits(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	if err := s.MarkLimited(ctx, "claude", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLimited(ctx, "claude", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Limited(ctx)
	if err != nil || len(got) != 1 || !got["claude"].Equal(t0.Add(time.Hour)) {
		t.Fatalf("limited: %v %v", got, err)
	}
	if err := s.ClearLimited(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	if got, err = s.Limited(ctx); err != nil || len(got) != 0 {
		t.Fatalf("cleared: %v %v", got, err)
	}
}

func TestMigration3KeepsTotals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	old.SetMaxOpenConns(1)
	if err := dbmigrate.Run(old, migrations[:2]); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO conversations (id, channel, thread, state, started, last, cost_usd, cost_known, in_tokens, out_tokens, model)
		VALUES (1, 'whatsapp', 'a', 'open', ?, ?, 0.05, 1, 40, 6, 'm'), (2, 'whatsapp', 'b', 'closed', ?, ?, 0, 1, 0, 0, '')`,
		ts(t0), ts(t0), ts(t0), ts(t0)); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, _, _, err := s.One(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if u := c.Total(""); u.CostUSD != 0.05 || !u.CostKnown || u.InTokens != 40 || u.OutTokens != 6 || u.Model != "m" {
		t.Fatalf("total: %+v", u)
	}
	if c, _, _, err = s.One(context.Background(), 2); err != nil || c.Total("") != (Usage{CostKnown: true}) {
		t.Fatalf("a conversation with no use: %+v %v", c, err)
	}
}
