package listen

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	listenv1 "concierge/engine/internal/gen/listen/v1"
)

// Replay returns the stored messages matching req at or after req.Since, oldest first.
type Replay func(ctx context.Context, req *listenv1.ListenRequest) ([]*listenv1.Message, error)

// Stream serves one listener: it subscribes to hub first, so nothing stored from then on slips
// past; with req.Since set it sends replay's matches; then one caught_up; then each live match,
// skipping the ones the replay already sent. It returns nil when ctx ends, and ResourceExhausted
// when the hub dropped the listener for falling behind (the client re-opens and the replay
// catches up).
func Stream(ctx context.Context, hub *Hub, req *listenv1.ListenRequest, replay Replay, send func(*listenv1.Event) error) error {
	sub := hub.Subscribe(req)
	defer sub.Close()

	replayed := map[int64]bool{}
	if req.GetSince() != nil {
		msgs, err := replay(ctx, req)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if err := send(message(m)); err != nil {
				return err
			}
			replayed[m.GetId()] = true
		}
	}
	if err := send(&listenv1.Event{Kind: &listenv1.Event_CaughtUp{CaughtUp: &listenv1.CaughtUp{}}}); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.Dropped():
			return fellBehind()
		case m := <-sub.Messages():
			select {
			case <-sub.Dropped(): // a dropped listener's buffered tail is not sent: the re-open replays it
				return fellBehind()
			default:
			}
			if replayed[m.GetId()] {
				delete(replayed, m.GetId()) // a message is published once
				continue
			}
			if err := send(message(m)); err != nil {
				return err
			}
		}
	}
}

// fellBehind ends the stream of a listener the hub dropped.
func fellBehind() error {
	return connect.NewError(connect.CodeResourceExhausted, errors.New("the listener fell behind"))
}

func message(m *listenv1.Message) *listenv1.Event {
	return &listenv1.Event{Kind: &listenv1.Event_Message{Message: m}}
}
