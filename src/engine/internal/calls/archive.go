package calls

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"concierge/engine/internal/dbmigrate"

	_ "modernc.org/sqlite"
)

// CallRecord: Outcome is answered, missed, declined, busy, no-answer, failed, unknown — or ringing /
// in-progress while the call lasts. Summary is the script's short summary of a call the box held (its
// check call). HasTurns / HasAudio say whether the debug keep (turns table, audio/<id>.wav) still
// holds that call's transcript and recording.
type CallRecord struct {
	ID         int64      `json:"id"`
	Number     string     `json:"number"`
	Direction  string     `json:"direction"`
	StartedAt  string     `json:"started_at"`
	AnsweredAt string     `json:"answered_at,omitempty"`
	EndedAt    string     `json:"ended_at,omitempty"`
	Outcome    string     `json:"outcome"`
	Summary    string     `json:"summary,omitempty"`
	Result     string     `json:"result,omitempty"`
	Prompt     string     `json:"prompt,omitempty"`
	Usage      *CallUsage `json:"usage,omitempty"`
	HasTurns   bool       `json:"has_turns,omitempty"`
	HasAudio   bool       `json:"has_audio,omitempty"`
}

// Turn is one thing said on a call: Speaker is "box" or "them".
type Turn struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	At      string `json:"at"`
}

type Archive struct {
	db       *sql.DB
	now      func() time.Time
	audioDir string
}

// migrations[v] brings calls.db from version v to v+1.
var migrations = []string{
	`CREATE TABLE calls (
		id INTEGER PRIMARY KEY, number TEXT NOT NULL, direction TEXT NOT NULL, started_at TEXT NOT NULL,
		answered_at TEXT NOT NULL DEFAULT '', ended_at TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL);
	CREATE INDEX calls_by_start ON calls (started_at);`,
	`ALTER TABLE calls ADD COLUMN heard TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE calls RENAME COLUMN heard TO summary;
	CREATE TABLE turns (call_id INTEGER NOT NULL, seq INTEGER NOT NULL, speaker TEXT NOT NULL, text TEXT NOT NULL, at TEXT NOT NULL, PRIMARY KEY (call_id, seq));`,
	`ALTER TABLE calls ADD COLUMN usage TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE calls ADD COLUMN result TEXT NOT NULL DEFAULT '';
	ALTER TABLE calls ADD COLUMN prompt TEXT NOT NULL DEFAULT '';`,
	// unseen: an incoming call the owner has not yet seen on the Voice page; calls kept so far start seen.
	`ALTER TABLE calls ADD COLUMN unseen INTEGER NOT NULL DEFAULT 0;`,
}

func OpenArchive(path string, now func() time.Time) (*Archive, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := dbmigrate.Run(db, migrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("calls schema: %w", err)
	}
	// Recordings of the other party's voice: owner-only, and tightened if an older directory was looser.
	audioDir := filepath.Join(filepath.Dir(path), "audio")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		db.Close()
		return nil, fmt.Errorf("calls audio directory: %w", err)
	}
	if err := os.Chmod(audioDir, 0o700); err != nil {
		db.Close()
		return nil, fmt.Errorf("calls audio directory: %w", err)
	}
	return &Archive{db: db, now: now, audioDir: audioDir}, nil
}

func (a *Archive) Close() error { return a.db.Close() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Start records a call as it begins (outcome "ringing").
func (a *Archive) Start(ctx context.Context, number, direction string, at time.Time) (int64, error) {
	res, err := a.db.ExecContext(ctx, `INSERT INTO calls (number, direction, started_at, outcome, unseen) VALUES (?,?,?,'ringing',?)`,
		number, direction, ts(at), direction == "incoming")
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Unseen is how many incoming calls the owner has not yet seen on the Voice page.
func (a *Archive) Unseen(ctx context.Context) (int, error) {
	var n int
	err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM calls WHERE unseen = 1`).Scan(&n)
	return n, err
}

// MarkSeen marks every call seen.
func (a *Archive) MarkSeen(ctx context.Context) error {
	_, err := a.db.ExecContext(ctx, `UPDATE calls SET unseen = 0 WHERE unseen = 1`)
	return err
}

func (a *Archive) MarkAnswered(ctx context.Context, id int64, at time.Time) error {
	_, err := a.db.ExecContext(ctx, `UPDATE calls SET answered_at = ?, outcome = 'in-progress' WHERE id = ?`, ts(at), id)
	return err
}

func (a *Archive) End(ctx context.Context, id int64, at time.Time, outcome string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE calls SET ended_at = ?, outcome = ? WHERE id = ?`, ts(at), outcome, id)
	return err
}

// SetSummary keeps the script's short summary of call id.
func (a *Archive) SetSummary(ctx context.Context, id int64, summary string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE calls SET summary = ? WHERE id = ?`, summary, id)
	return err
}

// SetResult keeps the script's outcome of call id and the prompt it was placed with.
func (a *Archive) SetResult(ctx context.Context, id int64, result, prompt string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE calls SET result = ?, prompt = ? WHERE id = ?`, result, prompt, id)
	return err
}

// SetUsage keeps the tokens call id's session used, as JSON.
func (a *Archive) SetUsage(ctx context.Context, id int64, u *CallUsage) error {
	b, err := json.Marshal(u)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `UPDATE calls SET usage = ? WHERE id = ?`, string(b), id)
	return err
}

// AddTurns appends turns to call id's transcript, after any already kept. A call whose row is gone
// keeps nothing: ids are reused once the newest rows are deleted, and the next call to get this one
// would inherit the turns.
func (a *Archive) AddTurns(ctx context.Context, id int64, turns []Turn) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range turns {
		if _, err := tx.ExecContext(ctx, `INSERT INTO turns (call_id, seq, speaker, text, at)
			SELECT ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM turns WHERE call_id = ?), ?, ?, ?
			WHERE EXISTS (SELECT 1 FROM calls WHERE id = ?)`,
			id, id, t.Speaker, t.Text, t.At, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Exists is whether call id has a row (Turns alone cannot tell an unknown call from a silent one).
func (a *Archive) Exists(ctx context.Context, id int64) (bool, error) {
	var ok bool
	err := a.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM calls WHERE id = ?)`, id).Scan(&ok)
	return ok, err
}

// Turns is call id's transcript in the order it was said.
func (a *Archive) Turns(ctx context.Context, id int64) ([]Turn, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT speaker, text, at FROM turns WHERE call_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Turn{}
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.Speaker, &t.Text, &t.At); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AudioPath is where call id's recording lives (the file may not exist).
func (a *Archive) AudioPath(id int64) string {
	return filepath.Join(a.audioDir, strconv.FormatInt(id, 10)+".wav")
}

// CloseLive closes calls left open by a previous run (calld stopped mid-call) as unknown.
func (a *Archive) CloseLive(ctx context.Context, at time.Time) error {
	_, err := a.db.ExecContext(ctx, `UPDATE calls SET ended_at = ?, outcome = 'unknown' WHERE outcome IN ('ringing', 'in-progress')`, ts(at))
	return err
}

// LiveCall is a call in progress: ringing or answered.
type LiveCall struct {
	ID        int64
	Direction string
}

// Live are the calls that have not ended, oldest first.
func (a *Archive) Live(ctx context.Context) ([]LiveCall, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, direction FROM calls WHERE outcome IN ('ringing', 'in-progress') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveCall
	for rows.Next() {
		var c LiveCall
		if err := rows.Scan(&c.ID, &c.Direction); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Calls are the newest `limit` calls, newest first.
func (a *Archive) Calls(ctx context.Context, limit int) ([]CallRecord, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, number, direction, started_at, answered_at, ended_at, outcome, summary, result, prompt, usage,
		EXISTS (SELECT 1 FROM turns WHERE call_id = calls.id)
		FROM calls ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CallRecord{}
	for rows.Next() {
		var c CallRecord
		var usage string
		if err := rows.Scan(&c.ID, &c.Number, &c.Direction, &c.StartedAt, &c.AnsweredAt, &c.EndedAt, &c.Outcome, &c.Summary, &c.Result, &c.Prompt, &usage, &c.HasTurns); err != nil {
			return nil, err
		}
		if usage != "" {
			c.Usage = new(CallUsage)
			if err := json.Unmarshal([]byte(usage), c.Usage); err != nil {
				return nil, err
			}
		}
		_, statErr := os.Stat(a.AudioPath(c.ID))
		c.HasAudio = statErr == nil
		out = append(out, c)
	}
	return out, rows.Err()
}

// Wipe deletes every stored call, with its turns and recordings (the owner removed Voice and chose
// to delete its data).
func (a *Archive) Wipe(ctx context.Context) error {
	if _, err := a.db.ExecContext(ctx, `DELETE FROM turns; DELETE FROM calls`); err != nil {
		return err
	}
	return a.sweepAudio(ctx)
}

// deleteStrayTurns drops turns whose call has no row, before a later call reusing the id inherits them.
const deleteStrayTurns = `DELETE FROM turns WHERE call_id NOT IN (SELECT id FROM calls)`

// Purge deletes calls that started longer than `days` ago (0 keeps everything).
func (a *Archive) Purge(ctx context.Context, days int) error {
	if days <= 0 {
		return nil
	}
	cut := ts(a.now().AddDate(0, 0, -days))
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM turns WHERE call_id IN (SELECT id FROM calls WHERE started_at < ?)`, cut); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM calls WHERE started_at < ?`, cut); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, deleteStrayTurns); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return a.sweepAudio(ctx) // the deleted calls' recordings go with them, whatever the debug retention
}

// PurgeDebug deletes the turns and recordings of calls that started longer than `days` ago, keeping
// their log rows (0 keeps everything), then sweeps turns and recordings whose call has no row.
func (a *Archive) PurgeDebug(ctx context.Context, days int) error {
	if days <= 0 {
		return nil
	}
	cut := ts(a.now().AddDate(0, 0, -days))
	if _, err := a.db.ExecContext(ctx, `DELETE FROM turns WHERE call_id IN (SELECT id FROM calls WHERE started_at < ?)`, cut); err != nil {
		return err
	}
	if _, err := a.db.ExecContext(ctx, deleteStrayTurns); err != nil {
		return err
	}
	rows, err := a.db.QueryContext(ctx, `SELECT id FROM calls WHERE started_at < ?`, cut)
	if err != nil {
		return err
	}
	var old []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		old = append(old, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range old {
		if err := os.Remove(a.AudioPath(id)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return a.sweepAudio(ctx)
}

// sweepAudio removes every <id>.wav whose call has no row; other files are left alone.
func (a *Archive) sweepAudio(ctx context.Context) error {
	entries, err := os.ReadDir(a.audioDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".wav") {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSuffix(e.Name(), ".wav"), 10, 64)
		if err != nil || e.Name() != strconv.FormatInt(id, 10)+".wav" { // "+5.wav", "05.wav" are not ours
			continue
		}
		var n int
		if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM calls WHERE id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if err := os.Remove(filepath.Join(a.audioDir, e.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
