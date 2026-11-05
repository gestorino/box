package sms

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/gen/listen/v1/listenv1connect"
	"concierge/engine/internal/listen"
)

// SetHub makes Save hand each newly stored incoming message to the hub's listeners.
func (a *Archive) SetHub(h *listen.Hub) { a.hub = h }

// message is a stored incoming SMS as a listener sees it.
func message(id int64, number, text, sentAt string, storedAt time.Time) *listenv1.Message {
	return &listenv1.Message{
		Id:       id,
		StoredAt: timestamppb.New(storedAt),
		Body:     &listenv1.Message_Sms{Sms: &listenv1.SMS{Number: number, Text: text, SentAt: sentAt}},
	}
}

// Since are the incoming messages stored at or after since, oldest first. since is compared as the
// archive writes it (whole seconds), so a message stored in since's own second is never missed.
func (a *Archive) Since(ctx context.Context, since time.Time) ([]*listenv1.Message, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, number, text, sent_at, stored_at FROM sms
		WHERE direction = 'in' AND stored_at >= ? ORDER BY stored_at ASC, id ASC`, ts(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*listenv1.Message
	for rows.Next() {
		var id int64
		var number, text, sentAt, storedAt string
		if err := rows.Scan(&id, &number, &text, &sentAt, &storedAt); err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339, storedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, message(id, number, text, sentAt, at))
	}
	return out, rows.Err()
}

type listenService struct {
	a   *Archive
	hub *listen.Hub
}

// NewListen is smsd's listen service: the engine opens a stream per listener and gets each
// matching incoming SMS as it is stored, after a replay from the archive when it asks for one.
// SMS has no recipient nor subject, so a filter on either is refused.
func NewListen(a *Archive, h *listen.Hub) listenv1connect.ListenServiceHandler {
	return &listenService{a: a, hub: h}
}

func (s *listenService) Listen(ctx context.Context, req *connect.Request[listenv1.ListenRequest], stream *connect.ServerStream[listenv1.Event]) error {
	if req.Msg.GetTo() != "" || req.Msg.GetSubjectContains() != "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("an SMS listener filters on from only"))
	}
	return listen.Stream(ctx, s.hub, req.Msg, s.replay, stream.Send)
}

func (s *listenService) replay(ctx context.Context, req *listenv1.ListenRequest) ([]*listenv1.Message, error) {
	msgs, err := s.a.Since(ctx, req.GetSince().AsTime())
	if err != nil {
		return nil, err
	}
	var out []*listenv1.Message
	for _, m := range msgs {
		if listen.MatchSMS(req, m) {
			out = append(out, m)
		}
	}
	return out, nil
}
