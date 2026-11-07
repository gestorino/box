package whatsapp

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/types/known/timestamppb"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/gen/listen/v1/listenv1connect"
	"concierge/engine/internal/listen"
)

// SetHub makes the service's live path hand each newly stored incoming private text to the hub's
// listeners.
func (a *Archive) SetHub(h *listen.Hub) { a.hub = h }

// privatePhone is the phone of a private chat on the phone-number server: a group, a broadcast, a
// newsletter and a hidden-number (@lid) chat have none.
func privatePhone(chatJID string) (string, bool) {
	j, err := types.ParseJID(chatJID)
	if err != nil || j.Server != types.DefaultUserServer || j.User == "" {
		return "", false
	}
	return j.User, true
}

func message(id int64, chat, phone, text string, sentAt, storedAt time.Time) *listenv1.Message {
	return &listenv1.Message{
		Id:       id,
		StoredAt: timestamppb.New(storedAt),
		Body: &listenv1.Message_Whatsapp{Whatsapp: &listenv1.WhatsApp{
			Chat: chat, Phone: phone, Text: text, SentAt: timestamppb.New(sentAt)}},
	}
}

// publish hands a just-stored row to the listeners if it is an incoming text of a private chat.
// Only the live path calls it: a history sync must never turn old chat into requests.
func (a *Archive) publish(ctx context.Context, r Row, rowid int64) {
	phone, ok := privatePhone(r.ChatJID)
	if a.hub == nil || !ok || r.FromMe || r.Text == "" {
		return
	}
	var stored string
	if err := a.db.QueryRowContext(ctx, `SELECT stored_at FROM messages WHERE rowid = ?`, rowid).Scan(&stored); err != nil {
		return
	}
	at, err := time.Parse(time.RFC3339, stored)
	if err != nil {
		return
	}
	a.hub.Publish(message(rowid, r.ChatJID, phone, r.Text, r.SentAt, at))
}

// Since are the incoming private text messages stored at or after since, oldest first. Rows
// stored before stored_at existed have none and are never replayed.
func (a *Archive) Since(ctx context.Context, since time.Time) ([]*listenv1.Message, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT rowid, chat_jid, text, sent_at, stored_at FROM messages
		WHERE from_me = 0 AND text <> '' AND stored_at <> '' AND stored_at >= ?
		ORDER BY stored_at ASC, rowid ASC`, ts(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*listenv1.Message
	for rows.Next() {
		var id int64
		var chat, text, sentAt, storedAt string
		if err := rows.Scan(&id, &chat, &text, &sentAt, &storedAt); err != nil {
			return nil, err
		}
		phone, ok := privatePhone(chat)
		if !ok {
			continue
		}
		sent, err := time.Parse(time.RFC3339, sentAt)
		if err != nil {
			return nil, err
		}
		stored, err := time.Parse(time.RFC3339, storedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, message(id, chat, phone, text, sent, stored))
	}
	return out, rows.Err()
}

type listenService struct {
	a   *Archive
	hub *listen.Hub
}

// NewListen is whatsappd's listen service: the engine opens a stream per listener and gets each
// matching incoming private text as it arrives, after a replay from the archive when it asks for one.
// A WhatsApp message has no recipient nor subject, so a filter on either is refused.
func NewListen(a *Archive, h *listen.Hub) listenv1connect.ListenServiceHandler {
	return &listenService{a: a, hub: h}
}

func (s *listenService) Listen(ctx context.Context, req *connect.Request[listenv1.ListenRequest], stream *connect.ServerStream[listenv1.Event]) error {
	if req.Msg.GetTo() != "" || req.Msg.GetSubjectContains() != "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("a WhatsApp listener filters on from only"))
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
		if listen.MatchWhatsApp(req, m) {
			out = append(out, m)
		}
	}
	return out, nil
}
