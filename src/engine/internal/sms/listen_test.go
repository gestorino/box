package sms

import (
	"context"
	"testing"
	"time"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/gen/listen/v1/listenv1connect"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/listen/listentest"
)

// listening opens a listener on the rig's archive, served as smsd serves it, and waits for its
// caught_up so whatever is stored next is live.
func listening(t *testing.T, g *rig, req *listenv1.ListenRequest) <-chan *listenv1.Event {
	t.Helper()
	hub := listen.NewHub(listen.MatchSMS)
	g.a.SetHub(hub)
	srv := listentest.Serve(t, NewListen(g.a, hub))
	c, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := listen.NewClient(srv.Socket).Listen(c, req)
	select {
	case e := <-events:
		if e.GetCaughtUp() == nil {
			t.Fatalf("first event %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no caught_up")
	}
	return events
}

func TestAStoredIncomingSMSReachesItsListener(t *testing.T) {
	g := newRig(t)
	events := listening(t, g, &listenv1.ListenRequest{From: "booksy"})
	g.c.t = t0.Add(700 * time.Millisecond) // stored_at keeps whole seconds: the live copy says t0 as the replay would
	g.a.Save(ctx, ModemSMS{Number: "BOOKSY", Text: "Code 123456", State: "received", Timestamp: t0.Format(time.RFC3339)}, false)
	select {
	case e := <-events:
		if s := e.GetMessage().GetSms(); s.GetText() != "Code 123456" || s.GetNumber() != "BOOKSY" || e.GetMessage().GetId() == 0 || !e.GetMessage().GetStoredAt().AsTime().Equal(t0) {
			t.Fatalf("event %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the listener got nothing")
	}
}

func TestAnOutgoingSMSReachesNoListener(t *testing.T) {
	g := newRig(t)
	events := listening(t, g, &listenv1.ListenRequest{})
	g.a.Save(ctx, ModemSMS{Number: "+351911111111", Text: "yo", State: "sent", Reference: 1}, false)
	select {
	case e := <-events:
		t.Fatalf("got %v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestTheSameSMSStoredTwiceIsPublishedOnce(t *testing.T) {
	g := newRig(t)
	events := listening(t, g, &listenv1.ListenRequest{})
	m := received("/SMS/1", "+351911111111", "hi", t0)
	g.a.Save(ctx, m, false)
	g.a.Save(ctx, m, false)
	g.a.Save(ctx, received("/SMS/2", "+351911111111", "again", t0), false)
	var texts []string
	for len(texts) < 2 {
		select {
		case e := <-events:
			texts = append(texts, e.GetMessage().GetSms().GetText())
		case <-time.After(5 * time.Second):
			t.Fatalf("got %v", texts)
		}
	}
	if texts[0] != "hi" || texts[1] != "again" {
		t.Fatalf("got %v", texts)
	}
}

func TestReplayRespectsSince(t *testing.T) {
	g := newRig(t)
	g.c.t = t0.Add(-time.Hour)
	g.a.Save(ctx, received("/SMS/1", "+351911111111", "old", t0.Add(-time.Hour)), false)
	g.c.t = t0
	g.a.Save(ctx, received("/SMS/2", "+351911111111", "new", t0), false)
	g.a.Save(ctx, ModemSMS{Number: "+351911111111", Text: "out", State: "sent", Reference: 1}, false)

	got, err := g.a.Since(ctx, t0.Add(-time.Minute))
	if err != nil || len(got) != 1 || got[0].GetSms().GetText() != "new" || !got[0].GetStoredAt().AsTime().Equal(t0) {
		t.Fatalf("%v, %v", got, err)
	}
	// Whole seconds: a message stored in the same second as since is replayed, never missed.
	if got, _ := g.a.Since(ctx, t0.Add(900*time.Millisecond)); len(got) != 1 {
		t.Fatalf("same second: %v", got)
	}
	if got, _ := g.a.Since(ctx, t0.Add(time.Second)); len(got) != 0 {
		t.Fatalf("after: %v", got)
	}
	if got, _ := g.a.Since(ctx, time.Time{}); len(got) != 2 {
		t.Fatalf("the whole archive: %v", got)
	}
}

func TestAnSMSListenerRefusesEmailFields(t *testing.T) {
	g := newRig(t)
	for _, req := range []*listenv1.ListenRequest{{To: "x"}, {SubjectContains: "x"}} {
		srv := listentest.Serve(t, NewListen(g.a, listen.NewHub(listen.MatchSMS)))
		c, cancel := context.WithCancel(context.Background())
		select {
		case e, ok := <-listen.NewClient(srv.Socket).Listen(c, req):
			if ok {
				t.Fatalf("got %v", e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the channel stayed open")
		}
		cancel()
	}
}

// ls is the listen service the handler tests mount.
func (g *rig) ls() listenv1connect.ListenServiceHandler {
	return NewListen(g.a, listen.NewHub(listen.MatchSMS))
}
