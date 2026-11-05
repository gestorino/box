package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// ReadMark is a chat the owner opened and WhatsApp has not yet been told: the messages to mark read,
// and the time of the newest.
type ReadMark struct {
	Chat string
	IDs  []string
	At   time.Time
}

// Unread counts the incoming private messages the owner has not opened.
func (a *Archive) Unread(ctx context.Context, account string) (int, error) {
	var n int
	err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE account = ? AND unread = 1`, account).Scan(&n)
	return n, err
}

// MarkChatRead marks a chat's unread messages read and queues them to be told to WhatsApp.
func (a *Archive) MarkChatRead(ctx context.Context, account, chat string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE messages SET unread = 0, mark_pending = 1
		WHERE account = ? AND chat_jid = ? AND unread = 1`, account, chat)
	return err
}

// PendingMarks are the account's messages waiting to be marked read on WhatsApp, a mark per chat. A
// mark queued under another account stays behind.
func (a *Archive) PendingMarks(ctx context.Context, account string) ([]ReadMark, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT chat_jid, id, sent_at FROM messages
		WHERE account = ? AND mark_pending = 1 ORDER BY chat_jid, sent_at, rowid`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReadMark
	for rows.Next() {
		var chat, id, sent string
		if err := rows.Scan(&chat, &id, &sent); err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339, sent)
		if err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].Chat != chat {
			out = append(out, ReadMark{Chat: chat})
		}
		m := &out[len(out)-1]
		m.IDs = append(m.IDs, id)
		if at.After(m.At) {
			m.At = at
		}
	}
	return out, rows.Err()
}

// ClearPending forgets that these messages await telling WhatsApp: it has been told.
func (a *Archive) ClearPending(ctx context.Context, account, chat string, ids []string) error {
	return a.updateIDs(ctx, `UPDATE messages SET mark_pending = 0
		WHERE account = ? AND chat_jid = ? AND id IN (SELECT value FROM json_each(?))`, account, chat, ids)
}

// ReadIDs marks messages read where WhatsApp already knows it (read on another device): nothing to tell.
func (a *Archive) ReadIDs(ctx context.Context, account, chat string, ids []string) error {
	return a.updateIDs(ctx, `UPDATE messages SET unread = 0, mark_pending = 0
		WHERE account = ? AND chat_jid = ? AND id IN (SELECT value FROM json_each(?))`, account, chat, ids)
}

// ReadThrough marks a chat's messages sent at or before at read, as ReadIDs does.
func (a *Archive) ReadThrough(ctx context.Context, account, chat string, at time.Time) error {
	_, err := a.db.ExecContext(ctx, `UPDATE messages SET unread = 0, mark_pending = 0
		WHERE account = ? AND chat_jid = ? AND sent_at <= ? AND (unread = 1 OR mark_pending = 1)`, account, chat, ts(at))
	return err
}

// updateIDs runs q with the ids as one JSON array parameter, so their number is no limit.
func (a *Archive) updateIDs(ctx context.Context, q, account, chat string, ids []string) error {
	list, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, q, account, chat, string(list))
	return err
}

// private is a chat with one person: not a group, broadcast or newsletter, and not the account's own.
func private(chat, account types.JID) bool {
	return (chat.Server == types.DefaultUserServer || chat.Server == types.HiddenUserServer) && chat.User != account.User
}

// Unread counts the incoming private messages the owner has not opened.
func (s *Service) Unread(ctx context.Context) (int, error) {
	return s.archive.Unread(ctx, s.c.Account())
}

// MarkRead is the owner opening a chat: its messages are read here at once, and WhatsApp is told in
// the background — never inside the request, so a half-dead connection holds nothing up. A group,
// broadcast or newsletter has nothing to count or tell.
func (s *Service) MarkRead(ctx context.Context, chat string) error {
	account := s.c.Account()
	jid, err := types.ParseJID(chat)
	self, selfErr := types.ParseJID(account)
	if err != nil || selfErr != nil || !private(jid, self) {
		return nil
	}
	if err := s.archive.MarkChatRead(ctx, account, chat); err != nil {
		return err
	}
	select {
	case s.nudge <- struct{}{}:
	default:
	}
	return nil
}

// markTimeout bounds one mark sent to WhatsApp, so a stuck send cannot hold flushing for good.
const markTimeout = 30 * time.Second

// flushMarks tells WhatsApp what the owner read here. A mark that fails stays pending for the next
// flush: after the next mark, a reconnect, or the minute.
func (s *Service) flushMarks(ctx context.Context) {
	s.flushing.Lock()
	defer s.flushing.Unlock()
	s.mu.Lock()
	up := s.state == "connected"
	s.mu.Unlock()
	account := s.c.Account()
	if !up || account == "" {
		return
	}
	marks, err := s.archive.PendingMarks(ctx, account)
	if err != nil {
		slog.Error("whatsapp: reading pending marks", "err", err)
		return
	}
	for _, m := range marks {
		chat, err := types.ParseJID(m.Chat)
		if err == nil {
			call, cancel := context.WithTimeout(ctx, s.markTimeout)
			err = s.c.MarkRead(call, m.IDs, m.At, chat)
			cancel()
		}
		if err != nil {
			// WhatsApp's error can name the chat: its type only.
			slog.Warn("whatsapp: marking read, will retry", "err_type", fmt.Sprintf("%T", err))
			continue
		}
		if err := s.archive.ClearPending(ctx, account, m.Chat, m.IDs); err != nil {
			slog.Error("whatsapp: clearing a sent mark", "err", err)
		}
	}
}

// flushLoop flushes on every nudge and each minute, until ctx (the daemon's) ends.
func (s *Service) flushLoop(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.nudge:
		}
		s.flushMarks(ctx)
	}
}
