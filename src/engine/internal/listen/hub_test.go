package listen

import (
	"testing"
	"time"

	listenv1 "concierge/engine/internal/gen/listen/v1"
)

func from(addr string) *listenv1.Message {
	return email(&listenv1.Email{FromAddr: addr})
}

func TestHubDeliversOnlyWhatMatches(t *testing.T) {
	h := NewHub(MatchEmail)
	a := h.Subscribe(&listenv1.ListenRequest{From: "a@x.example"})
	b := h.Subscribe(&listenv1.ListenRequest{From: "b@x.example"})
	h.Publish(from("a@x.example"))
	h.Publish(from("b@x.example"))

	for name, s := range map[string]*Sub{"a@x.example": a, "b@x.example": b} {
		select {
		case m := <-s.Messages():
			if got := m.GetEmail().GetFromAddr(); got != name {
				t.Errorf("sub %s got a message from %s", name, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("sub %s got nothing", name)
		}
		select {
		case m := <-s.Messages():
			t.Errorf("sub %s got a second message: %v", name, m)
		default:
		}
	}
}

func TestHubPublishNeverBlocksOnAFullSub(t *testing.T) {
	h := NewHub(MatchEmail)
	stuck := h.Subscribe(&listenv1.ListenRequest{})
	reader := h.Subscribe(&listenv1.ListenRequest{})
	got := make(chan int, 1)
	go func() {
		n := 0
		for range reader.Messages() {
			n++
			if n == subBuffer+1 {
				break
			}
		}
		got <- n
	}()

	done := make(chan struct{})
	go func() {
		for i := 0; i < subBuffer+1; i++ {
			h.Publish(from("a@x.example"))
			time.Sleep(time.Millisecond) // let the reader keep up, so only the stuck sub fills
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked")
	}
	select {
	case <-stuck.Dropped():
	default:
		t.Error("the full sub was not dropped")
	}
	select {
	case n := <-got:
		if n != subBuffer+1 {
			t.Errorf("reader got %d, want %d", n, subBuffer+1)
		}
	case <-time.After(time.Second):
		t.Fatal("the reading sub missed messages")
	}
}

func TestHubForgetsAClosedSub(t *testing.T) {
	h := NewHub(MatchEmail)
	s := h.Subscribe(&listenv1.ListenRequest{})
	if h.Len() != 1 {
		t.Fatalf("Len = %d, want 1", h.Len())
	}
	s.Close()
	s.Close() // idempotent
	h.Publish(from("a@x.example"))
	if h.Len() != 0 {
		t.Errorf("Len = %d, want 0", h.Len())
	}
}
