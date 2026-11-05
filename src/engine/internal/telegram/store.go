// Package telegram is telegramd: the box as a second device of the Telegram account on its SIM
// number. It archives the account's chats, sends what the owner sends from the console, and serves
// them to the engine over a Unix socket. Design: docs/superpowers/specs/2026-10-28-telegram-design.md
// and, for sending, docs/superpowers/specs/2026-10-28-connectors-write-and-verify-design.md.
package telegram

import (
	"context"
	"database/sql"
	"fmt"

	"concierge/engine/internal/dbmigrate"

	_ "modernc.org/sqlite"
)

// Store is telegram.db: the archive, the credentials, gotd's session and its update state.
type Store struct{ db *sql.DB }

// migrations[v] brings telegram.db from version v to v+1. A peer's read_inbox_max_id is the newest
// message id the owner has read (here or elsewhere); read_pending_id the newest read here that
// Telegram has not been told. Peers met before unread existed start read.
var migrations = []string{`
CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS channel_state (
	account TEXT NOT NULL, channel_id INTEGER NOT NULL, pts INTEGER NOT NULL, PRIMARY KEY (account, channel_id));
CREATE TABLE IF NOT EXISTS peers (
	account TEXT NOT NULL, peer TEXT NOT NULL, access_hash INTEGER NOT NULL DEFAULT 0, name TEXT NOT NULL DEFAULT '',
	username TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT '', PRIMARY KEY (account, peer));
CREATE TABLE IF NOT EXISTS messages (
	account TEXT NOT NULL, peer TEXT NOT NULL, id INTEGER NOT NULL, sender TEXT NOT NULL, sender_name TEXT NOT NULL,
	from_me INTEGER NOT NULL, sent_at TEXT NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL, file_name TEXT NOT NULL,
	PRIMARY KEY (account, peer, id));
CREATE INDEX IF NOT EXISTS messages_by_time ON messages (account, peer, sent_at);`,
	`ALTER TABLE peers ADD COLUMN read_inbox_max_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE peers ADD COLUMN read_pending_id INTEGER NOT NULL DEFAULT 0;
UPDATE peers SET read_inbox_max_id = COALESCE((SELECT MAX(id) FROM messages m
	WHERE m.account = peers.account AND m.peer = peers.peer), 0);`,
}

// OpenStore opens (creating if needed) the database at path: WAL, and synchronous=FULL so a stored
// message survives a power cut.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; reads are small
	if err := dbmigrate.Run(db, migrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("telegram schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Get is a config value, "" when unset.
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) Set(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO config (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
