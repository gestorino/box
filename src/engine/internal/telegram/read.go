package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// PendingRead is a private chat the owner opened and Telegram has not yet been told, read up to MaxID.
type PendingRead struct {
	Peer       string
	AccessHash int64
	MaxID      int
}

// Unread counts the incoming messages of private chats above the owner's read marker. What the owner
// sent, groups, channels and Saved Messages (self, its key) are never counted.
func (s *Store) Unread(ctx context.Context, account, self string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages m
		JOIN peers p ON p.account = m.account AND p.peer = m.peer
		WHERE m.account = ? AND p.kind = 'private' AND m.peer <> ? AND m.from_me = 0 AND m.id > p.read_inbox_max_id`,
		account, self).Scan(&n)
	return n, err
}

// MarkPeerRead is the owner opening a chat: the marker and the pending mark move to its newest archived
// message.
func (s *Store) MarkPeerRead(ctx context.Context, account, peer string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET
		read_inbox_max_id = MAX(read_inbox_max_id, newest), read_pending_id = MAX(read_pending_id, newest)
		FROM (SELECT COALESCE(MAX(id), 0) AS newest FROM messages WHERE account = ?1 AND peer = ?2)
		WHERE account = ?1 AND peer = ?2`, account, peer)
	return err
}

// SeedRead takes what Telegram says is read (a dialog's read_inbox_max_id) as the marker, though never
// below a mark Telegram has not been told yet.
func (s *Store) SeedRead(ctx context.Context, account, peer string, maxID int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET read_inbox_max_id = MAX(?, read_pending_id)
		WHERE account = ? AND peer = ?`, maxID, account, peer)
	return err
}

// ReadUpTo is Telegram saying the owner read a chat up to maxID on another device: the marker moves
// there, never back, and a pending mark that covers no more is moot.
func (s *Store) ReadUpTo(ctx context.Context, account, peer string, maxID int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET read_inbox_max_id = MAX(read_inbox_max_id, ?1),
		read_pending_id = CASE WHEN read_pending_id <= ?1 THEN 0 ELSE read_pending_id END
		WHERE account = ?2 AND peer = ?3`, maxID, account, peer)
	return err
}

// PendingReads are the account's chats waiting to be marked read on Telegram. A mark queued under
// another account stays behind.
func (s *Store) PendingReads(ctx context.Context, account string) ([]PendingRead, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT peer, access_hash, read_pending_id FROM peers
		WHERE account = ? AND read_pending_id > 0 ORDER BY peer`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingRead
	for rows.Next() {
		var p PendingRead
		if err := rows.Scan(&p.Peer, &p.AccessHash, &p.MaxID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClearPendingRead forgets that Telegram awaits a mark up to maxID: it has been told. A newer mark
// made meanwhile stays.
func (s *Store) ClearPendingRead(ctx context.Context, account, peer string, maxID int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET read_pending_id = 0
		WHERE account = ? AND peer = ? AND read_pending_id <= ?`, account, peer, maxID)
	return err
}

// selfKey is Saved Messages' key in the archive, linked or not connected.
func (s *Service) selfKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("user:%d", s.self.ID)
}

// Unread counts the incoming private messages the owner has not opened.
func (s *Service) Unread(ctx context.Context) (int, error) {
	account := s.account()
	if account == "" {
		return 0, nil
	}
	return s.store.Unread(ctx, account, s.selfKey())
}

// MarkRead is the owner opening a chat: its messages are read here at once, and Telegram is told in
// the background — never inside the request, so a half-dead connection holds nothing up. A group,
// channel or Saved Messages has nothing to count or tell.
func (s *Service) MarkRead(ctx context.Context, peer string) error {
	account := s.account()
	if account == "" || peer == s.selfKey() {
		return nil
	}
	p, ok, err := s.store.Peer(ctx, account, peer)
	if err != nil || !ok || p.Kind != "private" {
		return err
	}
	if err := s.store.MarkPeerRead(ctx, account, peer); err != nil {
		return err
	}
	select {
	case s.nudge <- struct{}{}:
	default:
	}
	return nil
}

// seedRead takes Telegram's read state as the markers, on each connect: the owner may have read
// chats on the phone while the box was down.
func (s *Service) seedRead(ctx context.Context) {
	account, self := s.account(), s.selfKey()
	if account == "" {
		return
	}
	err := s.c.Dialogs(ctx, func(d Dialog) error {
		if d.Peer.Kind != "private" || d.Peer.Key == self {
			return nil
		}
		return s.store.SeedRead(ctx, account, d.Peer.Key, d.ReadInboxMaxID)
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("telegram: reading the dialogs' read state", "err", err)
	}
}

// readTimeout bounds one mark sent to Telegram, so a stuck send cannot hold flushing for good.
const readTimeout = 30 * time.Second

// flushReads tells Telegram what the owner read here. A mark that fails stays pending for the next
// flush: after the next mark, a reconnect, or the minute.
func (s *Service) flushReads(ctx context.Context) {
	s.flushing.Lock()
	defer s.flushing.Unlock()
	s.mu.Lock()
	up := s.state == "connected"
	s.mu.Unlock()
	account := s.account()
	if !up || account == "" {
		return
	}
	marks, err := s.store.PendingReads(ctx, account)
	if err != nil {
		slog.Error("telegram: reading pending marks", "err", err)
		return
	}
	told := false
	for _, m := range marks {
		call, cancel := context.WithTimeout(ctx, s.readTimeout)
		err := s.c.ReadHistory(call, InputPeer{Key: m.Peer, AccessHash: m.AccessHash}, m.MaxID)
		cancel()
		if err != nil {
			// Telegram's error can name the peer: its type only.
			slog.Warn("telegram: marking read, will retry", "err_type", fmt.Sprintf("%T", err))
			continue
		}
		told = true
		if err := s.store.ClearPendingRead(ctx, account, m.Peer, m.MaxID); err != nil {
			slog.Error("telegram: clearing a sent mark", "err", err)
		}
	}
	if told { // reading may show the owner online for a moment, as sending does
		call, cancel := context.WithTimeout(ctx, s.readTimeout)
		defer cancel()
		if err := s.c.Offline(call); err != nil {
			slog.Warn("telegram: offline status after a mark", "err", err)
		}
	}
}

// flushLoop flushes on every nudge and each minute, until ctx (the service's life) ends.
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
		s.flushReads(ctx)
	}
}
