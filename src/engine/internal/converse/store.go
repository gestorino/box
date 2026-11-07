// Package converse is the engine's half of the owner's agent: its conversations with the owner, each
// on one channel and thread, and the tools the agent calls into the engine's own services. Design:
// docs/superpowers/specs/2026-11-07-agent-design.md (§2, §4).
package converse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"concierge/engine/internal/dbmigrate"
	agentv1 "concierge/engine/internal/gen/agent/v1"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

// IdleAfter is how long after its last event a conversation with no errand running closes.
const IdleAfter = 2 * time.Hour

const stateOpen = "open"

// ErrNoConversation is an id that names no conversation.
var ErrNoConversation = errors.New("no such conversation")

// Conversation is one conversation with the owner: its channel and thread, open or closed, when it
// started and when its last event was, and what the agent's steps used, added up. CostKnown is false
// once any step gave no cost: the total is then unknown, never a smaller number.
type Conversation struct {
	ID        int64     `json:"id"`
	Channel   string    `json:"channel"`
	Thread    string    `json:"thread"`
	State     string    `json:"state"`
	Started   time.Time `json:"started"`
	Last      time.Time `json:"last"`
	CostUSD   float64   `json:"cost_usd"`
	CostKnown bool      `json:"cost_known"`
	InTokens  int64     `json:"in_tokens"`
	OutTokens int64     `json:"out_tokens"`
	Model     string    `json:"model"`
}

// Store is the agent's own database (-agent-db): conversations, their events and the errands each
// started.
type Store struct {
	db *sql.DB
}

// migrations[v] brings agent.db from version v to v+1.
//
// conversation_events.event is the event as protojson, its id the row's. pending is 1 for an event the
// hub took in (the owner's message, an errand's event) whose turn has not finished: it is taken again
// after a restart, and the steps of the turns before it don't see it. confirm_run is the run of a
// confirm errand event (0 for every other event), confirm_wait its wait (signins' Question.Wait),
// confirm_told the conversation's last event once the owner was sent the Confirm (0 until then: an
// untold Confirm is never answerable), and answered_by the owner's event that answered it (0 while
// unanswered): the Confirm gate's state (tools.go).
var migrations = []string{`
CREATE TABLE conversations (
	id INTEGER PRIMARY KEY, channel TEXT NOT NULL, thread TEXT NOT NULL, state TEXT NOT NULL,
	started TEXT NOT NULL, last TEXT NOT NULL,
	cost_usd REAL NOT NULL DEFAULT 0, cost_known INTEGER NOT NULL DEFAULT 1,
	in_tokens INTEGER NOT NULL DEFAULT 0, out_tokens INTEGER NOT NULL DEFAULT 0, model TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX conversations_open ON conversations (channel, thread) WHERE state = 'open';
CREATE TABLE conversation_events (
	id INTEGER PRIMARY KEY, conversation INTEGER NOT NULL REFERENCES conversations (id), at TEXT NOT NULL,
	event TEXT NOT NULL, pending INTEGER NOT NULL DEFAULT 0,
	confirm_run INTEGER NOT NULL DEFAULT 0, confirm_wait INTEGER NOT NULL DEFAULT 0,
	confirm_told INTEGER NOT NULL DEFAULT 0, answered_by INTEGER NOT NULL DEFAULT 0);
CREATE INDEX conversation_events_by_conversation ON conversation_events (conversation, id);
CREATE INDEX conversation_events_pending ON conversation_events (id) WHERE pending = 1;
CREATE INDEX conversation_events_confirms ON conversation_events (conversation, confirm_run) WHERE confirm_run <> 0;
CREATE TABLE conversation_runs (
	run INTEGER PRIMARY KEY, conversation INTEGER NOT NULL REFERENCES conversations (id));
CREATE INDEX conversation_runs_by_conversation ON conversation_runs (conversation, run);`,
}

// OpenStore opens (creating if needed) the store at path: WAL, and synchronous=FULL so a stored event
// survives a power cut; one writer.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := dbmigrate.Run(db, migrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("agent schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(v string) (time.Time, error) { return time.Parse(time.RFC3339Nano, v) }

type scanner interface{ Scan(...any) error }

const conversationCols = `id, channel, thread, state, started, last, cost_usd, cost_known, in_tokens, out_tokens, model`

func scanConversation(r scanner) (Conversation, error) {
	var c Conversation
	var started, last string
	if err := r.Scan(&c.ID, &c.Channel, &c.Thread, &c.State, &started, &last, &c.CostUSD, &c.CostKnown,
		&c.InTokens, &c.OutTokens, &c.Model); err != nil {
		return Conversation{}, err
	}
	var err error
	if c.Started, err = parseTS(started); err != nil {
		return Conversation{}, err
	}
	c.Last, err = parseTS(last)
	return c, err
}

// Open is the channel's thread's open conversation, or a new one started at at.
func (s *Store) Open(ctx context.Context, channel, thread string, at time.Time) (Conversation, error) {
	tx, err := s.db.BeginTx(ctx, nil) // one connection: no other Open between the look and the insert
	if err != nil {
		return Conversation{}, err
	}
	defer tx.Rollback()
	c, err := scanConversation(tx.QueryRowContext(ctx, `SELECT `+conversationCols+` FROM conversations
		WHERE channel = ? AND thread = ? AND state = 'open'`, channel, thread))
	if !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO conversations (channel, thread, state, started, last)
		VALUES (?, ?, 'open', ?, ?)`, channel, thread, ts(at), ts(at))
	if err != nil {
		return Conversation{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Conversation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Conversation{}, err
	}
	return Conversation{ID: id, Channel: channel, Thread: thread, State: stateOpen, Started: at.UTC(), Last: at.UTC(),
		CostKnown: true}, nil
}

// Append keeps the event in the conversation at at, and moves the conversation's last event there.
// It sets the event's id to the stored one and returns it.
func (s *Store) Append(ctx context.Context, conv int64, e *agentv1.Event, at time.Time) (int64, error) {
	return s.append(ctx, conv, e, at, false)
}

// Intake appends an event the hub took in, pending until Taken says its turn finished.
func (s *Store) Intake(ctx context.Context, conv int64, e *agentv1.Event, at time.Time) (int64, error) {
	return s.append(ctx, conv, e, at, true)
}

// Taken marks the event's turn finished: it is no longer pending.
func (s *Store) Taken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversation_events SET pending = 0 WHERE id = ?`, id)
	return err
}

// Queued is an event still pending, and its conversation.
type Queued struct {
	Conversation int64
	Event        *agentv1.Event
}

// Pending is every event still pending, oldest first: the turns a restart cut short or never ran.
func (s *Store) Pending(ctx context.Context) ([]Queued, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, conversation, event FROM conversation_events WHERE pending = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Queued{}
	for rows.Next() {
		var id, conv int64
		var raw string
		if err := rows.Scan(&id, &conv, &raw); err != nil {
			return nil, err
		}
		e, err := decodeEvent(id, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, Queued{conv, e})
	}
	return out, rows.Err()
}

func (s *Store) append(ctx context.Context, conv int64, e *agentv1.Event, at time.Time, pending bool) (int64, error) {
	stored := proto.Clone(e).(*agentv1.Event)
	stored.Id = 0 // the row's id is the event's
	raw, err := protojson.Marshal(stored)
	if err != nil {
		return 0, err
	}
	var confirmRun, confirmWait int64
	if x := e.GetErrand(); x != nil && x.GetKind() == "confirm" {
		confirmRun, confirmWait = x.GetRun(), x.GetWait()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE conversations SET last = ? WHERE id = ?`, ts(at), conv)
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return 0, err
	} else if n == 0 {
		return 0, ErrNoConversation
	}
	res, err = tx.ExecContext(ctx, `INSERT INTO conversation_events (conversation, at, event, pending, confirm_run, confirm_wait)
		VALUES (?, ?, ?, ?, ?, ?)`, conv, ts(at), string(raw), pending, confirmRun, confirmWait)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	e.Id = id
	return id, nil
}

func decodeEvent(id int64, raw string) (*agentv1.Event, error) {
	e := &agentv1.Event{}
	if err := protojson.Unmarshal([]byte(raw), e); err != nil {
		return nil, fmt.Errorf("event %d: %w", id, err)
	}
	e.Id = id
	return e, nil
}

// Events is the conversation's last events, oldest first, each with its id; last <= 0 is all of them.
func (s *Store) Events(ctx context.Context, conv int64, last int) ([]*agentv1.Event, error) {
	return s.events(ctx, conv, last, -1)
}

// Window is what a turn taking the event #taking shows its agent: the conversation's last events but
// those still pending after it, which their own turns take later.
func (s *Store) Window(ctx context.Context, conv int64, last int, taking int64) ([]*agentv1.Event, error) {
	return s.events(ctx, conv, last, taking)
}

// events is the conversation's last events, leaving out those pending after #upTo (none left out
// when upTo < 0).
func (s *Store) events(ctx context.Context, conv int64, last int, upTo int64) ([]*agentv1.Event, error) {
	limit := -1 // SQLite: no limit
	if last > 0 {
		limit = last
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, event FROM (SELECT id, event FROM conversation_events
		WHERE conversation = ? AND (? < 0 OR pending = 0 OR id <= ?) ORDER BY id DESC LIMIT ?) ORDER BY id`,
		conv, upTo, upTo, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*agentv1.Event{}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		e, err := decodeEvent(id, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// event is one of the conversation's events; false when the conversation has none of that id.
func (s *Store) event(ctx context.Context, conv, id int64) (*agentv1.Event, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT event FROM conversation_events WHERE id = ? AND conversation = ?`,
		id, conv).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	e, err := decodeEvent(id, raw)
	return e, err == nil, err
}

// AddUsage adds a step's usage to the conversation's totals; its model becomes the conversation's.
func (s *Store) AddUsage(ctx context.Context, conv int64, u *agentv1.Usage) error {
	res, err := s.db.ExecContext(ctx, `UPDATE conversations SET cost_usd = cost_usd + ?, cost_known = cost_known AND ?,
		in_tokens = in_tokens + ?, out_tokens = out_tokens + ?, model = CASE WHEN ? = '' THEN model ELSE ? END
		WHERE id = ?`, u.GetCostUsd(), u.GetCostKnown(), u.GetInputTokens(), u.GetOutputTokens(), u.GetModel(), u.GetModel(), conv)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNoConversation
	}
	return nil
}

// LinkRun records that the conversation started the run: its events come back to it, and only it
// may answer, stop or ask after it.
func (s *Store) LinkRun(ctx context.Context, conv, run int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversation_runs (run, conversation) VALUES (?, ?)`, run, conv)
	return err
}

// ConversationOfRun is the conversation that started the run; false when none did.
func (s *Store) ConversationOfRun(ctx context.Context, run int64) (int64, bool, error) {
	var conv int64
	err := s.db.QueryRowContext(ctx, `SELECT conversation FROM conversation_runs WHERE run = ?`, run).Scan(&conv)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return conv, err == nil, err
}

func (s *Store) runsOf(ctx context.Context, conv int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run FROM conversation_runs WHERE conversation = ? ORDER BY run`, conv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var run int64
		if err := rows.Scan(&run); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// CloseIdle closes each open conversation whose last event is IdleAfter or more before now and none
// of whose runs is running.
func (s *Store) CloseIdle(ctx context.Context, now time.Time, running func(run int64) bool) error {
	cutoff := ts(now.Add(-IdleAfter))
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM conversations WHERE state = 'open' AND last <= ?`, cutoff)
	if err != nil {
		return err
	}
	var idle []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		idle = append(idle, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
next:
	for _, id := range idle {
		runs, err := s.runsOf(ctx, id)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if running(run) {
				continue next
			}
		}
		// Still idle: an event appended since the look keeps it open.
		if _, err := s.db.ExecContext(ctx, `UPDATE conversations SET state = 'closed' WHERE id = ? AND last <= ?`,
			id, cutoff); err != nil {
			return err
		}
	}
	return nil
}

// End closes the conversation, as the agent's end asks, unless one of its runs is running: it stays
// open while an errand it started runs.
func (s *Store) End(ctx context.Context, conv int64, running func(run int64) bool) error {
	runs, err := s.runsOf(ctx, conv)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if running(run) {
			return nil
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET state = 'closed' WHERE id = ?`, conv)
	return err
}

// conversation is one conversation, without its events; ErrNoConversation when there is none of
// that id.
func (s *Store) conversation(ctx context.Context, id int64) (Conversation, error) {
	c, err := scanConversation(s.db.QueryRowContext(ctx, `SELECT `+conversationCols+` FROM conversations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNoConversation
	}
	return c, err
}

// List is every conversation, the latest last event first.
func (s *Store) List(ctx context.Context) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+conversationCols+` FROM conversations ORDER BY last DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// One is a conversation with all its events and the runs it started; ErrNoConversation when there is
// none of that id.
func (s *Store) One(ctx context.Context, id int64) (Conversation, []*agentv1.Event, []int64, error) {
	c, err := s.conversation(ctx, id)
	if err != nil {
		return Conversation{}, nil, nil, err
	}
	events, err := s.Events(ctx, id, 0)
	if err != nil {
		return Conversation{}, nil, nil, err
	}
	runs, err := s.runsOf(ctx, id)
	if err != nil {
		return Conversation{}, nil, nil, err
	}
	return c, events, runs, nil
}

// tellConfirm marks the confirm event #id told: the owner was sent it, after the conversation's last
// event now. Only an owner's message after that mark answers it.
func (s *Store) tellConfirm(ctx context.Context, conv, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE conversation_events SET confirm_told =
		(SELECT max(id) FROM conversation_events WHERE conversation = ?)
		WHERE id = ? AND conversation = ? AND confirm_run <> 0`, conv, id, conv)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("event %d is no confirm of conversation %d", id, conv)
	}
	return nil
}

// pendingConfirm is the newest unanswered confirm the conversation was told for the run, its wait and
// the conversation's last event when it was told; false when there is none.
func (s *Store) pendingConfirm(ctx context.Context, conv, run int64) (id, wait, told int64, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT id, confirm_wait, confirm_told FROM conversation_events WHERE conversation = ?
		AND confirm_run = ? AND confirm_told <> 0 AND answered_by = 0 ORDER BY id DESC LIMIT 1`, conv, run).Scan(&id, &wait, &told)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, false, nil
	}
	return id, wait, told, err == nil, err
}

// The refusals of a claim on a Confirm.
var (
	errConfirmAnswered = refusal("that Confirm was answered already")
	errMessageUsed     = refusal("that message of the owner's answered a Confirm already: each Confirm takes a message of its own")
)

// claimConfirm marks the run's confirms in the conversation up to the confirm event as answered by
// the owner's event, once: errConfirmAnswered when it was answered already, errMessageUsed when the
// owner's event answered a confirm before.
func (s *Store) claimConfirm(ctx context.Context, conv, run, confirm, ownerEvent int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var used int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conversation_events WHERE answered_by = ?`, ownerEvent).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return errMessageUsed
	}
	res, err := tx.ExecContext(ctx, `UPDATE conversation_events SET answered_by = ? WHERE id = ? AND conversation = ?
		AND confirm_run = ? AND answered_by = 0`, ownerEvent, confirm, conv, run)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return errConfirmAnswered
	}
	// The older ones the newest stood for are answered with it: none can open again.
	if _, err := tx.ExecContext(ctx, `UPDATE conversation_events SET answered_by = ? WHERE conversation = ?
		AND confirm_run = ? AND answered_by = 0 AND id < ?`, ownerEvent, conv, run, confirm); err != nil {
		return err
	}
	return tx.Commit()
}

// unclaimConfirm takes back a claim whose answer the errand refused.
func (s *Store) unclaimConfirm(ctx context.Context, conv, run, ownerEvent int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversation_events SET answered_by = 0 WHERE conversation = ?
		AND confirm_run = ? AND answered_by = ?`, conv, run, ownerEvent)
	return err
}
