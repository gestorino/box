package converse

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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
	id, err := s.Append(context.Background(), conv, e, t0)
	if err != nil {
		t.Fatal(err)
	}
	return id
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
		id, err := s.Append(ctx, conv, e, t0.Add(time.Duration(i)*time.Minute))
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
	if _, err := s.Append(ctx, conv+100, owner("lost"), t0); err == nil {
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
		if _, err := s.Append(ctx, conv, owner("still here"), later); err != nil {
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
	if err := s.AddUsage(ctx, conv, &agentv1.Usage{Model: "m1", InputTokens: 100, OutputTokens: 10, CostUsd: 0.25, CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, conv, &agentv1.Usage{Model: "m2", InputTokens: 50, OutputTokens: 5, CostUsd: 0.5, CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	c, _, _, err := s.One(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	if c.InTokens != 150 || c.OutTokens != 15 || c.CostUSD != 0.75 || !c.CostKnown || c.Model != "m2" {
		t.Fatalf("two usages: %+v", c)
	}
	if err := s.AddUsage(ctx, conv, &agentv1.Usage{Model: "m2", InputTokens: 1, OutputTokens: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, conv, &agentv1.Usage{Model: "m2", InputTokens: 1, OutputTokens: 1, CostUsd: 1, CostKnown: true}); err != nil {
		t.Fatal(err)
	}
	if c, _, _, _ = s.One(ctx, conv); c.CostKnown || c.InTokens != 152 {
		t.Fatalf("an unknown cost: %+v", c)
	}
}

func TestOneAndListAndTheRunsOfAConversation(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	first, second := open(t, s, "a"), open(t, s, "b")
	appendEvent(t, s, first, owner("hello"))
	if _, err := s.Append(ctx, second, owner("later"), t0.Add(time.Minute)); err != nil {
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
	if c.ID != first || len(events) != 1 || events[0].GetOwner() != "hello" || len(runs) != 2 || runs[0] != 3 || runs[1] != 5 {
		t.Fatalf("one: %+v %v %v", c, events, runs)
	}
	if conv, ok, err := s.ConversationOfRun(ctx, 5); err != nil || !ok || conv != first {
		t.Fatalf("run 5: %d %v %v", conv, ok, err)
	}
	if _, ok, err := s.ConversationOfRun(ctx, 6); err != nil || ok {
		t.Fatalf("run 6: %v %v", ok, err)
	}
	list, err := s.List(ctx)
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
