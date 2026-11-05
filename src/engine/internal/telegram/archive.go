package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Row is one message as stored. Account is the linked user's id, so two accounts never mix; Peer
// is "user:<id>", "chat:<id>" or "channel:<id>".
type Row struct {
	Account, Peer      string
	ID                 int
	Sender, SenderName string
	FromMe             bool
	SentAt             time.Time
	Kind, Text         string
	FileName           string
}

// Peer is a user, group or channel the account has met. Kind is private, group or channel.
type Peer struct {
	Account, Key   string
	AccessHash     int64
	Name, Username string
	Kind           string
}

type Chat struct {
	Peer     string `json:"peer"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	LastText string `json:"last_text"`
	LastKind string `json:"last_kind"`
	LastAt   string `json:"last_at"`
	Count    int    `json:"count"`
	Unread   int    `json:"unread"`
}

type Msg struct {
	ID         int    `json:"id"`
	SenderName string `json:"sender_name"`
	FromMe     bool   `json:"from_me"`
	SentAt     string `json:"sent_at"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	FileName   string `json:"file_name,omitempty"`
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Save stores a message once (backfill and live delivery can both bring it).
func (s *Store) Save(ctx context.Context, r Row) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO messages
		(account, peer, id, sender, sender_name, from_me, sent_at, kind, text, file_name)
		VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		r.Account, r.Peer, r.ID, r.Sender, r.SenderName, r.FromMe, ts(r.SentAt), r.Kind, r.Text, r.FileName)
	return err
}

// Edit overwrites a stored message with its latest version; a message never stored stays absent.
func (s *Store) Edit(ctx context.Context, r Row) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET kind = ?, text = ?, file_name = ?
		WHERE account = ? AND peer = ? AND id = ?`, r.Kind, r.Text, r.FileName, r.Account, r.Peer, r.ID)
	return err
}

// DeleteCommon removes messages by id from private chats and basic groups, which share one id
// sequence per account (Telegram's updateDeleteMessages names no peer). Channels number their own.
func (s *Store) DeleteCommon(ctx context.Context, account string, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	q, args := inIDs(`DELETE FROM messages WHERE account = ? AND (peer LIKE 'user:%' OR peer LIKE 'chat:%') AND id IN `, ids, account)
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// DeleteChannel removes messages by id from one supergroup or channel.
func (s *Store) DeleteChannel(ctx context.Context, account string, channelID int64, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	q, args := inIDs(`DELETE FROM messages WHERE account = ? AND peer = ? AND id IN `, ids, account, fmt.Sprintf("channel:%d", channelID))
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// inIDs completes a query ending in "id IN " with one placeholder per id, after the lead arguments.
func inIDs(prefix string, ids []int, lead ...any) (string, []any) {
	args := lead
	for _, id := range ids {
		args = append(args, id)
	}
	return prefix + "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")", args
}

// PutPeer records what Telegram said about a peer; an empty name or username, or a zero access
// hash, never erases a known one.
func (s *Store) PutPeer(ctx context.Context, p Peer) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO peers (account, peer, access_hash, name, username, kind) VALUES (?,?,?,?,?,?)
		ON CONFLICT (account, peer) DO UPDATE SET
			access_hash = CASE WHEN excluded.access_hash <> 0 THEN excluded.access_hash ELSE peers.access_hash END,
			name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE peers.name END,
			username = CASE WHEN excluded.username <> '' THEN excluded.username ELSE peers.username END,
			kind = CASE WHEN excluded.kind <> '' THEN excluded.kind ELSE peers.kind END`,
		p.Account, p.Key, p.AccessHash, p.Name, p.Username, p.Kind)
	return err
}

func (s *Store) Peer(ctx context.Context, account, key string) (Peer, bool, error) {
	p := Peer{Account: account, Key: key}
	err := s.db.QueryRowContext(ctx, `SELECT access_hash, name, username, kind FROM peers WHERE account = ? AND peer = ?`,
		account, key).Scan(&p.AccessHash, &p.Name, &p.Username, &p.Kind)
	if err == sql.ErrNoRows {
		return Peer{}, false, nil
	}
	return p, err == nil, err
}

// Chats are the account's chats that hold messages, newest last message first, with that message
// and a count, and the incoming messages the owner has not opened (see Unread). self is Saved
// Messages' key.
func (s *Store) Chats(ctx context.Context, account, self string) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH last AS (
			SELECT peer, kind, text, sent_at, COUNT(*) OVER (PARTITION BY peer) AS n,
				ROW_NUMBER() OVER (PARTITION BY peer ORDER BY sent_at DESC, rowid DESC) AS rn
			FROM messages WHERE account = ?)
		SELECT l.peer, COALESCE(p.name, ''), COALESCE(p.kind, ''), l.text, l.kind, l.sent_at, l.n,
			CASE WHEN p.kind = 'private' AND l.peer <> ? THEN
				(SELECT COUNT(*) FROM messages m WHERE m.account = ? AND m.peer = l.peer AND m.from_me = 0
					AND m.id > p.read_inbox_max_id)
			ELSE 0 END
		FROM last l LEFT JOIN peers p ON p.account = ? AND p.peer = l.peer
		WHERE l.rn = 1 ORDER BY l.sent_at DESC`, account, self, account, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.Peer, &c.Name, &c.Kind, &c.LastText, &c.LastKind, &c.LastAt, &c.Count, &c.Unread); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Messages are a chat's newest `limit` messages, oldest first.
func (s *Store) Messages(ctx context.Context, account, peer string, limit int) ([]Msg, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, sender_name, from_me, sent_at, kind, text, file_name FROM (
			SELECT rowid AS r, * FROM messages WHERE account = ? AND peer = ?
			ORDER BY sent_at DESC, id DESC LIMIT ?)
		ORDER BY sent_at ASC, id ASC`, account, peer, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Msg{}
	for rows.Next() {
		var m Msg
		if err := rows.Scan(&m.ID, &m.SenderName, &m.FromMe, &m.SentAt, &m.Kind, &m.Text, &m.FileName); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Wipe empties telegram.db: every account's messages, peers and update state, and the config — the
// credentials, the session and the settings.
func (s *Store) Wipe(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM messages`, `DELETE FROM peers`, `DELETE FROM channel_state`, `DELETE FROM config`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteAccount removes one account's peers, messages and channel update state, nothing else.
func (s *Store) DeleteAccount(ctx context.Context, account string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM messages WHERE account = ?`,
		`DELETE FROM peers WHERE account = ?`,
		`DELETE FROM channel_state WHERE account = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, account); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Broadcasts are the ids of the account's broadcast channels.
func (s *Store) Broadcasts(ctx context.Context, account string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT peer FROM peers WHERE account = ? AND kind = 'channel'`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		_, id := splitKey(key)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
