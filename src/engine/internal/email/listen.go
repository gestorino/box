package email

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/gen/listen/v1/listenv1connect"
	"concierge/engine/internal/listen"
)

// SetHub makes Save hand each newly stored incoming email to the hub's listeners.
func (a *Archive) SetHub(h *listen.Hub) { a.hub = h }

// messageOf is a stored incoming email as a listener sees it. The HTML is the first text/html body
// part ("" when raw can't be split); the date is the email's own Date header.
func messageOf(id int64, storedAt time.Time, p Parsed, raw []byte, atts []Attachment) *listenv1.Message {
	e := &listenv1.Email{
		FromName: p.FromName, FromAddr: p.FromAddr, To: p.To, Cc: p.Cc, Subject: p.Subject, Text: p.Text,
	}
	if parts, err := split(raw); err == nil {
		e.Html = string(parts.html)
		if d, err := parts.header.Date(); err == nil && !d.IsZero() {
			e.Date = timestamppb.New(d)
		}
	}
	for _, at := range atts {
		e.Attachments = append(e.Attachments, &listenv1.Attachment{
			Idx: int32(at.Idx), Filename: at.Filename, ContentType: at.ContentType, Size: at.Size})
	}
	return &listenv1.Message{Id: id, StoredAt: timestamppb.New(storedAt), Body: &listenv1.Message_Email{Email: e}}
}

// Since are the account's incoming emails matching req stored at or after req.Since, oldest first.
// Since is compared as the archive writes it (whole seconds), so an email stored in its own second
// is never missed. The sender and recipient are narrowed in SQL; MatchEmail decides.
func (a *Archive) Since(ctx context.Context, account string, req *listenv1.ListenRequest) ([]*listenv1.Message, error) {
	q := `SELECT id, stored_at, raw FROM emails WHERE account = ? AND direction = 'in' AND stored_at >= ?`
	args := []any{account, ts(req.GetSince().AsTime())}
	if req.GetFrom() != "" {
		q += ` AND lower(from_addr) = lower(?)`
		args = append(args, req.GetFrom())
	}
	if req.GetTo() != "" {
		q += ` AND instr(lower(to_addrs), lower(?)) > 0`
		args = append(args, req.GetTo())
	}
	rows, err := a.db.QueryContext(ctx, q+` ORDER BY stored_at ASC, id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Scanned first, parsed after: the rows end (and give back the archive's only connection)
	// before any email is MIME-parsed.
	type row struct {
		id       int64
		storedAt string
		raw      []byte
	}
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.storedAt, &r.raw); err != nil {
			return nil, err
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []*listenv1.Message
	for _, r := range found {
		at, err := time.Parse(time.RFC3339, r.storedAt)
		if err != nil { // one bad row never fails the whole replay
			slog.Warn("email: skipped a stored email whose stored_at does not parse", "id", r.id)
			continue
		}
		p, _ := Parse(r.raw, account) // a malformed email was stored empty and matches no address
		if m := messageOf(r.id, at, p, r.raw, p.Attachments); listen.MatchEmail(req, m) {
			out = append(out, m)
		}
	}
	return out, nil
}

type listenService struct {
	s   *Service
	a   *Archive
	hub *listen.Hub
}

// NewListen is emaild's listen service: the engine opens a stream per listener and gets each
// matching incoming email as it is stored, after a replay from the archive when it asks for one.
// A listener names the address it waits at (To), so there is no listening to the whole mailbox.
func NewListen(s *Service, a *Archive, h *listen.Hub) listenv1connect.ListenServiceHandler {
	return &listenService{s: s, a: a, hub: h}
}

func (l *listenService) Listen(ctx context.Context, req *connect.Request[listenv1.ListenRequest], stream *connect.ServerStream[listenv1.Event]) error {
	if req.Msg.GetTo() == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("an email listener names the address it waits at (to)"))
	}
	acct, err := l.a.Current(ctx)
	if err != nil {
		return err
	}
	if st := l.s.Status().State; acct == nil || st == "disconnected" || st == "auth_failed" {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("no mailbox is connected"))
	}
	return listen.Stream(ctx, l.hub, req.Msg, l.replay, stream.Send)
}

func (l *listenService) replay(ctx context.Context, req *listenv1.ListenRequest) ([]*listenv1.Message, error) {
	acct, err := l.a.Current(ctx)
	if err != nil {
		return nil, err
	}
	if acct == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no mailbox is connected"))
	}
	return l.a.Since(ctx, acct.Address, req)
}
