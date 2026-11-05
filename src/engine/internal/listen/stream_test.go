package listen

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	listenv1 "concierge/engine/internal/gen/listen/v1"
)

// sends runs Stream until it has sent want events, then ends it and returns what it sent.
func sends(t *testing.T, hub *Hub, req *listenv1.ListenRequest, replay Replay, want int) []*listenv1.Event {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var got []*listenv1.Event
	err := Stream(ctx, hub, req, replay, func(e *listenv1.Event) error {
		got = append(got, e)
		if len(got) == want {
			cancel()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	return got
}

func TestStreamSkipsALiveMessageItReplayed(t *testing.T) {
	hub := NewHub(MatchSMS)
	m := sms("+351912345678")
	m.Id = 7
	n := sms("+351912345678")
	n.Id = 8
	replay := func(context.Context, *listenv1.ListenRequest) ([]*listenv1.Message, error) {
		hub.Publish(m) // stored and published after the subscribe, before the archive read
		hub.Publish(n)
		return []*listenv1.Message{m}, nil
	}
	got := sends(t, hub, &listenv1.ListenRequest{Since: timestamppb.Now()}, replay, 3)
	if len(got) != 3 || got[0].GetMessage().GetId() != 7 || got[1].GetCaughtUp() == nil || got[2].GetMessage().GetId() != 8 {
		t.Errorf("got %v, want 7, caught_up, 8", got)
	}
}

func TestStreamEndsWithResourceExhaustedWhenDropped(t *testing.T) {
	hub := NewHub(MatchSMS)
	sent := 0
	err := Stream(t.Context(), hub, &listenv1.ListenRequest{}, nil, func(e *listenv1.Event) error {
		sent++
		if sent == 1 {
			for range subBuffer + 1 { // overflow the sub's buffer while Stream is not reading
				hub.Publish(sms("+351912345678"))
			}
		}
		return nil
	})
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("err = %v, want ResourceExhausted", err)
	}
	if sent != 1 {
		t.Errorf("sent %d events, want only caught_up: a dropped listener's tail is not sent", sent)
	}
	if hub.Len() != 0 {
		t.Errorf("Len = %d, want 0", hub.Len())
	}
}
