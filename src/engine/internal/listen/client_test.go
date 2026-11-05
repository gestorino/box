package listen_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/listen/listentest"
)

// setup serves a fake smsd and returns a client of it that re-opens quickly.
func setup(t *testing.T) (*listentest.Fake, *listentest.Server, *listen.Client) {
	t.Helper()
	fake := listentest.NewFake(listen.MatchSMS)
	server := listentest.Serve(t, fake)
	c := listen.NewClient(server.Socket)
	listen.SetBackoff(c, 10*time.Millisecond)
	return fake, server, c
}

func text(s string) *listenv1.Message {
	return &listenv1.Message{Body: &listenv1.Message_Sms{Sms: &listenv1.SMS{Number: "+351912345678", Text: s}}}
}

func next(t *testing.T, events <-chan *listenv1.Event) *listenv1.Event {
	t.Helper()
	select {
	case e, ok := <-events:
		if !ok {
			t.Fatal("the listener closed")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
		return nil
	}
}

func wantCaughtUp(t *testing.T, e *listenv1.Event) {
	t.Helper()
	if e.GetCaughtUp() == nil {
		t.Fatalf("got %v, want caught_up", e)
	}
}

func wantText(t *testing.T, e *listenv1.Event, want string) {
	t.Helper()
	if got := e.GetMessage().GetSms().GetText(); got != want {
		t.Fatalf("got %v, want message %q", e, want)
	}
}

// eventually polls cond until it holds or a deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var everything = &listenv1.ListenRequest{Since: timestamppb.New(time.Time{})}

func TestLiveOnlyGetsCaughtUpAtOnceAndNothingStoredBefore(t *testing.T) {
	fake, _, c := setup(t)
	fake.Add(text("before"))
	events := c.Listen(t.Context(), &listenv1.ListenRequest{})
	wantCaughtUp(t, next(t, events))
	fake.Add(text("after"))
	wantText(t, next(t, events), "after")
}

func TestReplayIsOldestFirstThenCaughtUpThenLive(t *testing.T) {
	fake, _, c := setup(t)
	fake.Add(text("A"))
	fake.Add(text("B"))
	events := c.Listen(t.Context(), everything)
	wantText(t, next(t, events), "A")
	wantText(t, next(t, events), "B")
	wantCaughtUp(t, next(t, events))
	fake.Add(text("C"))
	wantText(t, next(t, events), "C")
}

func TestAMessageStoredDuringReplayArrivesOnce(t *testing.T) {
	fake, _, c := setup(t)
	fake.OnReplay(func() { fake.Add(text("M")) })
	events := c.Listen(t.Context(), everything)

	var got []string
	caughtUp := false
	for !caughtUp {
		e := next(t, events)
		if e.GetCaughtUp() != nil {
			caughtUp = true
			continue
		}
		got = append(got, e.GetMessage().GetSms().GetText())
	}
	fake.Add(text("N"))
	for {
		e := next(t, events)
		if e.GetCaughtUp() != nil {
			t.Fatal("a second caught_up")
		}
		s := e.GetMessage().GetSms().GetText()
		if s == "N" {
			break
		}
		got = append(got, s)
	}
	if !slices.Equal(got, []string{"M"}) {
		t.Errorf("got %q, want M exactly once", got)
	}
}

func TestTheClientReopensAndCatchesUp(t *testing.T) {
	fake, _, c := setup(t)
	events := c.Listen(t.Context(), &listenv1.ListenRequest{})
	wantCaughtUp(t, next(t, events))
	fake.Add(text("L")) // delivered live; the re-open's replay brings it again
	wantText(t, next(t, events), "L")

	fake.Fail(connect.CodeUnavailable) // the client can't re-open until M is stored
	fake.Kick()
	eventually(t, "the kicked stream stayed open", func() bool { return fake.Listeners() == 0 })
	// Stored while the client is away: only the re-open's replay brings it, after L again.
	fake.Add(text("M"))
	fake.Fail(0)
	wantText(t, next(t, events), "M")

	fake.Add(text("N"))
	wantText(t, next(t, events), "N") // not L or M again, not a second caught_up
}

func TestTheClientRetriesARefusedOpen(t *testing.T) {
	fake, _, c := setup(t)
	fake.Fail(connect.CodeFailedPrecondition)
	events := c.Listen(t.Context(), &listenv1.ListenRequest{})
	time.Sleep(50 * time.Millisecond)
	fake.Fail(0)
	fake.Add(text("M"))
	for {
		e := next(t, events)
		if e.GetCaughtUp() != nil {
			continue
		}
		wantText(t, e, "M")
		return
	}
}

func TestCancelEndsTheListener(t *testing.T) {
	fake, _, c := setup(t)
	ctx, cancel := context.WithCancel(t.Context())
	events := c.Listen(ctx, &listenv1.ListenRequest{})
	wantCaughtUp(t, next(t, events))
	if n := fake.Listeners(); n != 1 {
		t.Fatalf("Listeners = %d, want 1", n)
	}

	cancel()
	deadline := time.After(5 * time.Second)
	for closed := false; !closed; {
		select {
		case _, ok := <-events:
			closed = !ok
		case <-deadline:
			t.Fatal("the channel stayed open")
		}
	}
	eventually(t, "the fake still holds the listener", func() bool { return fake.Listeners() == 0 })
}

func TestTheClientSpeaksHTTP2(t *testing.T) {
	_, server, c := setup(t)
	wantCaughtUp(t, next(t, c.Listen(t.Context(), &listenv1.ListenRequest{})))
	if got := server.Protocols(); !slices.Equal(got, []string{"HTTP/2.0"}) {
		t.Errorf("Protocols = %q, want [HTTP/2.0]", got)
	}
}
