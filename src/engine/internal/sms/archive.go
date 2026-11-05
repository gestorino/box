package sms

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"concierge/engine/internal/dbmigrate"
	"concierge/engine/internal/listen"

	_ "modernc.org/sqlite"
)

type Conversation struct {
	Number        string `json:"number"`
	LastText      string `json:"last_text"`
	LastAt        string `json:"last_at"`
	LastDirection string `json:"last_direction"`
	LastIsData    bool   `json:"last_is_data"`
	Count         int    `json:"count"`
	Unread        int    `json:"unread"`
}

type Message struct {
	ID         int64  `json:"id"`
	Direction  string `json:"direction"`
	Number     string `json:"number"`
	Text       string `json:"text"`
	SentAt     string `json:"sent_at"`
	DataBytes  int    `json:"data_bytes,omitempty"`
	Incomplete bool   `json:"incomplete,omitempty"`
}

type Archive struct {
	db  *sql.DB
	now func() time.Time
	hub *listen.Hub // set before the archiver runs; nil publishes nowhere
}

// migrations[v] brings sms.db from version v to v+1. Messages stored before unread existed start read.
var migrations = []string{`
CREATE TABLE IF NOT EXISTS sms (
	id INTEGER PRIMARY KEY, direction TEXT NOT NULL, number TEXT NOT NULL, text TEXT NOT NULL,
	data BLOB, pdu_type TEXT NOT NULL, incomplete INTEGER NOT NULL, sent_at TEXT NOT NULL,
	stored_at TEXT NOT NULL, dedupe TEXT NOT NULL UNIQUE);
CREATE INDEX IF NOT EXISTS sms_by_number ON sms (number, sent_at);
CREATE INDEX IF NOT EXISTS sms_by_stored ON sms (stored_at);`,
	`ALTER TABLE sms ADD COLUMN unread INTEGER NOT NULL DEFAULT 0;`,
}

func OpenArchive(path string, now func() time.Time) (*Archive, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := dbmigrate.Run(db, migrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("sms schema: %w", err)
	}
	return &Archive{db: db, now: now}, nil
}

func (a *Archive) Close() error { return a.db.Close() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func direction(state string) string {
	if state == "sent" {
		return "out"
	}
	return "in"
}

// Save stores a message once (its dedupe hash survives ModemManager renumbering) and returns the hash.
func (a *Archive) Save(ctx context.Context, m ModemSMS, incomplete bool) (string, error) {
	dir, sentAt, when := direction(m.State), m.Timestamp, m.Timestamp
	if sentAt == "" { // most sent messages: stamped now, keyed by their message reference
		sentAt, when = ts(a.now()), fmt.Sprintf("mr:%d", m.Reference)
	}
	h := sha256.New()
	for _, part := range [][]byte{[]byte(dir), []byte(m.Number), []byte(when), []byte(m.Text), m.Data} {
		h.Write(part)
		h.Write([]byte{0})
	}
	dedupe := hex.EncodeToString(h.Sum(nil))
	stored := a.now().UTC().Truncate(time.Second) // as stored_at keeps it, so live and replayed copies agree
	res, err := a.db.ExecContext(ctx, `INSERT INTO sms
		(direction, number, text, data, pdu_type, incomplete, sent_at, stored_at, dedupe, unread)
		VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT (dedupe) DO NOTHING`,
		dir, m.Number, m.Text, m.Data, m.PDUType, incomplete, sentAt, ts(stored), dedupe, dir == "in")
	if err != nil {
		return dedupe, err
	}
	if n, _ := res.RowsAffected(); n == 1 && dir == "in" && a.hub != nil {
		id, _ := res.LastInsertId()
		a.hub.Publish(message(id, m.Number, m.Text, sentAt, stored))
	}
	return dedupe, nil
}

// Has confirms a stored row before its modem copy may be deleted.
func (a *Archive) Has(ctx context.Context, dedupe string) (bool, error) {
	var n int
	err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sms WHERE dedupe = ?`, dedupe).Scan(&n)
	return n == 1, err
}

// Conversations: one per number, newest last message first, either direction.
func (a *Archive) Conversations(ctx context.Context) ([]Conversation, error) {
	rows, err := a.db.QueryContext(ctx, `
		WITH ranked AS (
			SELECT number, text, direction, sent_at, length(data) AS data_len,
				ROW_NUMBER() OVER (PARTITION BY number ORDER BY sent_at DESC, id DESC) AS rn,
				COUNT(*) OVER (PARTITION BY number) AS cnt,
				SUM(unread) OVER (PARTITION BY number) AS unread
			FROM sms)
		SELECT number, text, direction, sent_at, COALESCE(data_len, 0) > 0 AND text = '', cnt, unread
		FROM ranked WHERE rn = 1 ORDER BY sent_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.Number, &c.LastText, &c.LastDirection, &c.LastAt, &c.LastIsData, &c.Count, &c.Unread); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Unread counts the incoming messages the owner has not opened.
func (a *Archive) Unread(ctx context.Context) (int, error) {
	var n int
	err := a.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(unread), 0) FROM sms`).Scan(&n)
	return n, err
}

// MarkRead marks every message of number read ("" is the unknown sender).
func (a *Archive) MarkRead(ctx context.Context, number string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE sms SET unread = 0 WHERE number = ? AND unread = 1`, number)
	return err
}

func (a *Archive) messages(ctx context.Context, query string, args ...any) ([]Message, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, direction, number, text, sent_at, COALESCE(length(data), 0), incomplete
		FROM sms `+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Direction, &m.Number, &m.Text, &m.SentAt, &m.DataBytes, &m.Incomplete); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Thread is one number's messages, oldest first.
func (a *Archive) Thread(ctx context.Context, number string) ([]Message, error) {
	return a.messages(ctx, `WHERE number = ? ORDER BY sent_at ASC, id ASC`, number)
}

// Recent are the incoming messages stored within `since`, newest first (the WhatsApp setup looks for
// its verification code here).
func (a *Archive) Recent(ctx context.Context, since time.Duration) ([]Message, error) {
	return a.messages(ctx, `WHERE direction = 'in' AND stored_at >= ? ORDER BY stored_at DESC, id DESC`, ts(a.now().Add(-since)))
}

// Wipe deletes every stored message (the owner removed SMS and chose to delete its data). The modem
// is not touched.
func (a *Archive) Wipe(ctx context.Context) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM sms`)
	return err
}

// Purge deletes what the box has kept longer than `days` (0 keeps everything) — by when it was
// stored, never by the message centre's clock.
func (a *Archive) Purge(ctx context.Context, days int) error {
	if days <= 0 {
		return nil
	}
	_, err := a.db.ExecContext(ctx, `DELETE FROM sms WHERE stored_at < ?`, ts(a.now().AddDate(0, 0, -days)))
	return err
}
