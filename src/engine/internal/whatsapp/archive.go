// Package whatsapp is whatsappd: the box as a linked WhatsApp device for its SIM's account. It
// archives every message, sends what the owner sends from the console, and serves them to the engine
// over a Unix socket. Design: docs/superpowers/specs/2026-10-28-whatsapp-design.md and, for sending,
// docs/superpowers/specs/2026-10-28-connectors-write-and-verify-design.md.
package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"concierge/engine/internal/dbmigrate"
	"concierge/engine/internal/listen"

	_ "modernc.org/sqlite"
)

// Row is one message as stored. Account is the linked number's JID, so two accounts never mix.
type Row struct {
	Account, ChatJID, ID, SenderJID, SenderName string
	FromMe                                      bool
	SentAt                                      time.Time
	Kind, Text, FileName                        string
	Unread                                      bool // an incoming private message the owner has not opened
	Live                                        bool // received live, not from a history sync: only these reach listeners
}

type Chat struct {
	JID      string `json:"jid"`
	Name     string `json:"name"`
	IsGroup  bool   `json:"is_group"`
	LastText string `json:"last_text"`
	LastKind string `json:"last_kind"`
	LastAt   string `json:"last_at"`
	Count    int    `json:"count"`
	Unread   int    `json:"unread"`
}

type Msg struct {
	ID         string `json:"id"`
	SenderName string `json:"sender_name"`
	FromMe     bool   `json:"from_me"`
	SentAt     string `json:"sent_at"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	FileName   string `json:"file_name,omitempty"`
}

type Archive struct {
	db  *sql.DB
	now func() time.Time
	hub *listen.Hub // set before the service runs; nil publishes nowhere
}

// migrations[v] brings archive.db from version v to v+1. Messages stored before unread existed start read.
var migrations = []string{`
CREATE TABLE IF NOT EXISTS chats (
	account TEXT NOT NULL, jid TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', is_group INTEGER NOT NULL DEFAULT 0,
	last_at TEXT NOT NULL DEFAULT '', PRIMARY KEY (account, jid));
CREATE TABLE IF NOT EXISTS messages (
	account TEXT NOT NULL, chat_jid TEXT NOT NULL, id TEXT NOT NULL, sender_jid TEXT NOT NULL,
	sender_name TEXT NOT NULL, from_me INTEGER NOT NULL, sent_at TEXT NOT NULL, kind TEXT NOT NULL,
	text TEXT NOT NULL, file_name TEXT NOT NULL, PRIMARY KEY (account, chat_jid, id));
CREATE INDEX IF NOT EXISTS messages_by_time ON messages (account, chat_jid, sent_at);
CREATE TABLE IF NOT EXISTS state (key TEXT PRIMARY KEY, value TEXT NOT NULL);`,
	// mark_pending: the owner opened the chat and WhatsApp has not yet been told.
	`ALTER TABLE messages ADD COLUMN unread INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN mark_pending INTEGER NOT NULL DEFAULT 0;
CREATE INDEX messages_unread ON messages (account, chat_jid) WHERE unread = 1;`,
	// stored_at: when the box stored the row (listeners replay by it); rows from before keep ''.
	`ALTER TABLE messages ADD COLUMN stored_at TEXT NOT NULL DEFAULT '';
CREATE INDEX messages_stored ON messages (stored_at) WHERE stored_at <> '';`,
	// live: the row arrived live, never by history sync; listeners replay only these.
	`ALTER TABLE messages ADD COLUMN live INTEGER NOT NULL DEFAULT 0;`,
}

// OpenArchive opens (creating if needed) the archive at path: WAL, and synchronous=FULL so a stored
// message survives a power cut.
func OpenArchive(path string) (*Archive, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; reads are small
	if err := dbmigrate.Run(db, migrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("archive schema: %w", err)
	}
	return &Archive{db: db, now: time.Now}, nil
}

func (a *Archive) Close() error { return a.db.Close() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Save stores a message once (history sync and live delivery can both bring it) and moves its chat's
// last_at forward. inserted says it was new, rowid is then its row (0 when it was already stored).
func (a *Archive) Save(ctx context.Context, r Row) (inserted bool, rowid int64, err error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO messages
		(account, chat_jid, id, sender_jid, sender_name, from_me, sent_at, kind, text, file_name, unread, stored_at, live)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		r.Account, r.ChatJID, r.ID, r.SenderJID, r.SenderName, r.FromMe, ts(r.SentAt), r.Kind, r.Text, r.FileName, r.Unread, ts(a.now()), r.Live)
	if err != nil {
		return false, 0, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		inserted = true
		rowid, _ = res.LastInsertId()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chats (account, jid, last_at) VALUES (?,?,?)
		ON CONFLICT (account, jid) DO UPDATE SET last_at = MAX(last_at, excluded.last_at)`,
		r.Account, r.ChatJID, ts(r.SentAt)); err != nil {
		return false, 0, err
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	return inserted, rowid, nil
}

// Update replaces a stored message's text: an edit, or a deletion for everyone.
func (a *Archive) Update(ctx context.Context, account, chat, id, text string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE messages SET text = ? WHERE account = ? AND chat_jid = ? AND id = ?`,
		text, account, chat, id)
	return err
}

// NameChat records a chat's name; an empty name never erases a known one.
func (a *Archive) NameChat(ctx context.Context, account, jid, name string, isGroup bool) error {
	_, err := a.db.ExecContext(ctx, `INSERT INTO chats (account, jid, name, is_group) VALUES (?,?,?,?)
		ON CONFLICT (account, jid) DO UPDATE SET
			name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE chats.name END,
			is_group = excluded.is_group`, account, jid, name, isGroup)
	return err
}

// Chats are the account's chats, newest last message first, with that message and a count.
func (a *Archive) Chats(ctx context.Context, account string) ([]Chat, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT c.jid, c.name, c.is_group, c.last_at,
			(SELECT COUNT(*) FROM messages m WHERE m.account = c.account AND m.chat_jid = c.jid),
			COALESCE((SELECT m.text FROM messages m WHERE m.account = c.account AND m.chat_jid = c.jid
				ORDER BY m.sent_at DESC, m.rowid DESC LIMIT 1), ''),
			COALESCE((SELECT m.kind FROM messages m WHERE m.account = c.account AND m.chat_jid = c.jid
				ORDER BY m.sent_at DESC, m.rowid DESC LIMIT 1), ''),
			(SELECT COUNT(*) FROM messages m WHERE m.account = c.account AND m.chat_jid = c.jid AND m.unread = 1)
		FROM chats c WHERE c.account = ? AND c.last_at <> ''
		ORDER BY c.last_at DESC`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.JID, &c.Name, &c.IsGroup, &c.LastAt, &c.Count, &c.LastText, &c.LastKind, &c.Unread); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Messages are a chat's newest `limit` messages, oldest first.
func (a *Archive) Messages(ctx context.Context, account, chat string, limit int) ([]Msg, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, sender_name, from_me, sent_at, kind, text, file_name FROM (
			SELECT rowid AS r, * FROM messages WHERE account = ? AND chat_jid = ?
			ORDER BY sent_at DESC, rowid DESC LIMIT ?)
		ORDER BY sent_at ASC, r ASC`, account, chat, limit)
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

// DeleteAccount removes one account's chats and messages, nothing else.
func (a *Archive) DeleteAccount(ctx context.Context, account string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM messages WHERE account = ?`, `DELETE FROM chats WHERE account = ?`} {
		if _, err := tx.ExecContext(ctx, q, account); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Wipe removes every account's chats and messages.
func (a *Archive) Wipe(ctx context.Context) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM messages`, `DELETE FROM chats`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// State is the persisted link state ("logged_out", or "" for none).
func (a *Archive) State(ctx context.Context) (string, error) {
	var v string
	err := a.db.QueryRowContext(ctx, `SELECT value FROM state WHERE key = 'link'`).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (a *Archive) SetState(ctx context.Context, s string) error {
	_, err := a.db.ExecContext(ctx, `INSERT INTO state (key, value) VALUES ('link', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, s)
	return err
}
