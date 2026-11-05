package email

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	netmail "net/mail"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"concierge/engine/internal/dbmigrate"
	"concierge/engine/internal/listen"
)

// Cursor is how far the box has read the mailbox. LastUID is the highest UID of new mail stored or
// passed over; the first copy runs over (CopiedUID, CopyUntilUID] and is done when they meet.
type Cursor struct {
	UIDValidity, LastUID, CopiedUID, CopyUntilUID uint32
}

func (c Cursor) Copying() bool { return c.CopiedUID < c.CopyUntilUID }

// Account is the mailbox the box follows. State is disconnected, connected, copying,
// paused_mobile or auth_failed.
type Account struct {
	Address string
	Cursor
	CopyOnMobile bool
	State        string
}

type Thread struct {
	Thread         string   `json:"thread"`
	Subject        string   `json:"subject"`
	Participants   []string `json:"participants"`
	LastSnippet    string   `json:"last_snippet"`
	LastDirection  string   `json:"last_direction"`
	LastAt         string   `json:"last_at"`
	Count          int      `json:"count"`
	HasAttachments bool     `json:"has_attachments"`
	Unread         int      `json:"unread"` // its unread incoming emails
}

type Email struct {
	ID          int64        `json:"id"`
	Direction   string       `json:"direction"`
	FromName    string       `json:"from_name"`
	FromAddr    string       `json:"from_addr"`
	To          []string     `json:"to"`
	Cc          []string     `json:"cc"`
	Subject     string       `json:"subject"`
	Date        string       `json:"date"`
	Text        string       `json:"text"`
	HasHTML     bool         `json:"has_html"`
	Attachments []Attachment `json:"attachments"`
}

type Archive struct {
	db  *sql.DB
	now func() time.Time
	hub *listen.Hub // set before the copy runs; nil publishes nowhere
}

// migrations[v] brings email.db from version v to v+1. Mail stored before unread existed starts read;
// the first reconcile then takes Gmail's state.
var migrations = []string{`
CREATE TABLE IF NOT EXISTS accounts (
	address TEXT PRIMARY KEY, app_password TEXT NOT NULL, uidvalidity INTEGER NOT NULL DEFAULT 0,
	last_uid INTEGER NOT NULL DEFAULT 0, copied_uid INTEGER NOT NULL DEFAULT 0,
	copy_until_uid INTEGER NOT NULL DEFAULT 0,
	copy_on_mobile INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL, connected_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS emails (
	id INTEGER PRIMARY KEY, account TEXT NOT NULL, dedupe TEXT NOT NULL, uid INTEGER NOT NULL,
	message_id TEXT NOT NULL, thread TEXT NOT NULL, direction TEXT NOT NULL, from_name TEXT NOT NULL,
	from_addr TEXT NOT NULL, to_addrs TEXT NOT NULL, cc_addrs TEXT NOT NULL, subject TEXT NOT NULL,
	date TEXT NOT NULL, snippet TEXT NOT NULL, text TEXT NOT NULL, has_html INTEGER NOT NULL,
	raw BLOB NOT NULL, UNIQUE (account, dedupe));
CREATE INDEX IF NOT EXISTS emails_by_thread ON emails (account, thread, date);
CREATE INDEX IF NOT EXISTS emails_by_date ON emails (date);
CREATE TABLE IF NOT EXISTS attachments (
	email_id INTEGER NOT NULL REFERENCES emails (id) ON DELETE CASCADE, idx INTEGER NOT NULL,
	filename TEXT NOT NULL, content_type TEXT NOT NULL, size INTEGER NOT NULL,
	PRIMARY KEY (email_id, idx));`,
	`ALTER TABLE emails ADD COLUMN unread INTEGER NOT NULL DEFAULT 0;
ALTER TABLE emails ADD COLUMN mark_pending INTEGER NOT NULL DEFAULT 0;
CREATE INDEX emails_unread ON emails (account, thread) WHERE unread = 1;
CREATE INDEX emails_uid ON emails (account, uid);
CREATE INDEX emails_pending ON emails (account) WHERE mark_pending = 1;`,
	`ALTER TABLE emails ADD COLUMN stored_at TEXT NOT NULL DEFAULT '';
UPDATE emails SET stored_at = date;
CREATE INDEX emails_stored ON emails (account, stored_at);`,
	`ALTER TABLE accounts DROP COLUMN app_password;`,
}

func OpenArchive(path string, now func() time.Time) (*Archive, error) {
	db, err := sql.Open("sqlite", "file:"+path+
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// auto_vacuum only takes effect on a database with no tables yet, so it comes first.
	if _, err := db.Exec(`PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("email schema: %w", err)
	}
	if err := dbmigrate.Run(db, migrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("email schema: %w", err)
	}
	return &Archive{db: db, now: now}, nil
}

func (a *Archive) Close() error { return a.db.Close() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (a *Archive) PutAccount(ctx context.Context, acct Account) error {
	_, err := a.db.ExecContext(ctx, `INSERT INTO accounts
		(address, uidvalidity, last_uid, copied_uid, copy_until_uid, copy_on_mobile, state, connected_at)
		VALUES (?,?,?,?,?,?,?,?) ON CONFLICT (address) DO UPDATE SET
		uidvalidity = excluded.uidvalidity, last_uid = excluded.last_uid,
		copied_uid = excluded.copied_uid, copy_until_uid = excluded.copy_until_uid, copy_on_mobile = excluded.copy_on_mobile,
		state = excluded.state, connected_at = excluded.connected_at`,
		acct.Address, acct.UIDValidity, acct.LastUID, acct.CopiedUID, acct.CopyUntilUID, acct.CopyOnMobile,
		acct.State, ts(a.now()))
	return err
}

const accountCols = `address, uidvalidity, last_uid, copied_uid, copy_until_uid, copy_on_mobile, state`

func scanAccount(row *sql.Row) (*Account, error) {
	var acct Account
	err := row.Scan(&acct.Address, &acct.UIDValidity, &acct.LastUID, &acct.CopiedUID,
		&acct.CopyUntilUID, &acct.CopyOnMobile, &acct.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &acct, nil
}

// Current is the most recently connected account, connected or not; nil when there is none.
func (a *Archive) Current(ctx context.Context) (*Account, error) {
	return scanAccount(a.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts
		ORDER BY connected_at DESC, rowid DESC LIMIT 1`))
}

// Account is the account with this address; nil when there is none.
func (a *Archive) Account(ctx context.Context, address string) (*Account, error) {
	return scanAccount(a.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE address = ?`, address))
}

func (a *Archive) SetCursor(ctx context.Context, address string, c Cursor) error {
	_, err := a.db.ExecContext(ctx, `UPDATE accounts SET uidvalidity = ?, last_uid = ?, copied_uid = ?,
		copy_until_uid = ? WHERE address = ?`, c.UIDValidity, c.LastUID, c.CopiedUID, c.CopyUntilUID, address)
	return err
}

// Renumber moves the cursor to a renumbered mailbox. Every UID the account's emails hold is from the
// old numbering and now names other mail (or none): they become 0 and read, with their pending marks
// kept, until Save finds each email again under its new UID.
func (a *Archive) Renumber(ctx context.Context, address string, c Cursor) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET uidvalidity = ?, last_uid = ?, copied_uid = ?,
		copy_until_uid = ? WHERE address = ?`, c.UIDValidity, c.LastUID, c.CopiedUID, c.CopyUntilUID, address); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE emails SET uid = 0, unread = 0 WHERE account = ?`, address); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *Archive) SetState(ctx context.Context, address, state string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE accounts SET state = ? WHERE address = ?`, state, address)
	return err
}

// Forget disconnects the account (it stays readable); with deleteEmails it
// removes the account and every email it holds.
func (a *Archive) Forget(ctx context.Context, address string, deleteEmails bool) error {
	if !deleteEmails {
		_, err := a.db.ExecContext(ctx, `UPDATE accounts SET state = 'disconnected'
			WHERE address = ?`, address)
		return err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM emails WHERE account = ?`, `DELETE FROM accounts WHERE address = ?`} {
		if _, err := tx.ExecContext(ctx, q, address); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	return err
}

// ForgetPasswords disconnects every account; their emails stay readable.
func (a *Archive) ForgetPasswords(ctx context.Context) error {
	_, err := a.db.ExecContext(ctx, `UPDATE accounts SET state = 'disconnected'`)
	return err
}

// Wipe deletes every account and every email (attachments go with them).
func (a *Archive) Wipe(ctx context.Context) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM emails`, `DELETE FROM accounts`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	return err
}

// Save stores an email once per account (keyed by its bytes), with its attachments' list.
func (a *Archive) Save(ctx context.Context, account string, uid uint32, date time.Time, raw []byte) error {
	p, err := Parse(raw, account)
	if err != nil { // kept as it came, so one malformed email never stops the copy at its UID
		slog.Warn("email: stored an email that could not be parsed", "uid", uid) // the error quotes the email
		p = Parsed{}
	}
	sum := sha256.Sum256(raw)
	dedupe := hex.EncodeToString(sum[:])
	thread := p.Thread
	if thread == "" {
		thread = "<" + dedupe + ">"
	}
	direction := "in"
	if p.Out {
		direction = "out"
	}
	to, _ := json.Marshal(p.To)
	cc, _ := json.Marshal(p.Cc)
	stored := a.now().UTC().Truncate(time.Second) // as stored_at keeps it, so live and replayed copies agree
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO emails (account, dedupe, uid, message_id, thread, direction,
		from_name, from_addr, to_addrs, cc_addrs, subject, date, stored_at, snippet, text, has_html, raw)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT (account, dedupe) DO NOTHING`,
		account, dedupe, uid, p.MessageID, thread, direction, p.FromName, p.FromAddr, string(to), string(cc),
		p.Subject, ts(date), ts(stored), p.Snippet, p.Text, p.HasHTML, raw)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 { // stored before; a renumbered mailbox gives it a new UID
		if _, err := tx.ExecContext(ctx, `UPDATE emails SET uid = ? WHERE account = ? AND dedupe = ?`, uid, account, dedupe); err != nil {
			return err
		}
		return tx.Commit()
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, at := range p.Attachments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO attachments (email_id, idx, filename, content_type, size)
			VALUES (?,?,?,?,?)`, id, at.Idx, at.Filename, at.ContentType, at.Size); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if direction == "in" && a.hub != nil {
		a.hub.Publish(messageOf(id, stored, p, raw, p.Attachments))
	}
	return nil
}

func (a *Archive) Count(ctx context.Context, account string) (int, error) {
	var n int
	err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM emails WHERE account = ?`, account).Scan(&n)
	return n, err
}

// Threads are the account's conversations, newest activity first.
func (a *Archive) Threads(ctx context.Context, account string) ([]Thread, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT thread, subject, direction, from_name, from_addr, snippet, date,
		EXISTS (SELECT 1 FROM attachments WHERE email_id = emails.id), unread
		FROM emails WHERE account = ? ORDER BY date, id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*Thread{}
	var order []*Thread
	for rows.Next() {
		var thread, subject, direction, fromName, fromAddr, snippet, date string
		var hasAtt, unread bool
		if err := rows.Scan(&thread, &subject, &direction, &fromName, &fromAddr, &snippet, &date, &hasAtt, &unread); err != nil {
			return nil, err
		}
		t, ok := byID[thread]
		if !ok {
			t = &Thread{Thread: thread, Subject: subject, Participants: []string{}}
			byID[thread] = t
			order = append(order, t)
		}
		who := fromName
		switch {
		case direction == "out":
			who = "me"
		case who == "":
			who = fromAddr
		}
		if !contains(t.Participants, who) {
			t.Participants = append(t.Participants, who)
		}
		t.Count++
		t.LastSnippet, t.LastDirection, t.LastAt = snippet, direction, date
		t.HasAttachments = t.HasAttachments || hasAtt
		if unread {
			t.Unread++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Thread, len(order))
	for i, t := range order {
		out[i] = *t
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastAt > out[j].LastAt })
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Thread is one conversation's emails, oldest first, each with its attachments' list.
func (a *Archive) Thread(ctx context.Context, account, thread string) ([]Email, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, direction, from_name, from_addr, to_addrs, cc_addrs, subject,
		date, text, has_html FROM emails WHERE account = ? AND thread = ? ORDER BY date, id`, account, thread)
	if err != nil {
		return nil, err
	}
	out := []Email{}
	for rows.Next() {
		var e Email
		var to, cc string
		if err := rows.Scan(&e.ID, &e.Direction, &e.FromName, &e.FromAddr, &to, &cc, &e.Subject, &e.Date, &e.Text, &e.HasHTML); err != nil {
			rows.Close()
			return nil, err
		}
		json.Unmarshal([]byte(to), &e.To)
		json.Unmarshal([]byte(cc), &e.Cc)
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Attachments, err = a.attachments(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (a *Archive) attachments(ctx context.Context, id int64) ([]Attachment, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT idx, filename, content_type, size FROM attachments
		WHERE email_id = ? ORDER BY idx`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var at Attachment
		if err := rows.Scan(&at.Idx, &at.Filename, &at.ContentType, &at.Size); err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, rows.Err()
}

// Raw is the original message of one of the account's emails; sql.ErrNoRows when it has none.
func (a *Archive) Raw(ctx context.Context, account string, id int64) ([]byte, error) {
	var raw []byte
	err := a.db.QueryRowContext(ctx, `SELECT raw FROM emails WHERE account = ? AND id = ?`, account, id).Scan(&raw)
	return raw, err
}

// Purge removes every account's emails dated more than days ago (0 keeps them all) and gives the
// space back.
func (a *Archive) Purge(ctx context.Context, days int) error {
	if days <= 0 {
		return nil
	}
	if _, err := a.db.ExecContext(ctx, `DELETE FROM emails WHERE date < ?`, ts(a.now().AddDate(0, 0, -days))); err != nil {
		return err
	}
	_, err := a.db.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	return err
}

// ReplyTarget is how a reply in thread is addressed: to the sender of its newest email (its
// Reply-To when it names one), or — when the owner sent that one — to its first recipient; "Re: "
// once; in reply to that email.
func (a *Archive) ReplyTarget(ctx context.Context, account, thread string) (Outgoing, error) {
	var direction, fromAddr, to, subject, messageID string
	var raw []byte
	err := a.db.QueryRowContext(ctx, `SELECT direction, from_addr, to_addrs, subject, message_id, raw FROM emails
		WHERE account = ? AND thread = ? ORDER BY date DESC, id DESC LIMIT 1`, account, thread).
		Scan(&direction, &fromAddr, &to, &subject, &messageID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Outgoing{}, errNotFound
	}
	if err != nil {
		return Outgoing{}, err
	}
	out := Outgoing{To: fromAddr, Subject: subject, InReplyTo: messageID}
	if direction == "in" {
		if m, err := netmail.ReadMessage(bytes.NewReader(raw)); err == nil {
			if list, err := m.Header.AddressList("Reply-To"); err == nil && len(list) > 0 {
				out.To = list[0].Address
			}
		}
	}
	if direction == "out" {
		var list []string
		json.Unmarshal([]byte(to), &list)
		if len(list) == 0 {
			return Outgoing{}, errNotFound
		}
		addr, err := netmail.ParseAddress(list[0])
		if err != nil {
			return Outgoing{}, errNotFound
		}
		out.To = addr.Address
	}
	if !strings.HasPrefix(strings.ToLower(out.Subject), "re:") {
		out.Subject = "Re: " + out.Subject
	}
	return out, nil
}
