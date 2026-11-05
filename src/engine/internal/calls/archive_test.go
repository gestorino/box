package calls

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"concierge/engine/internal/realtime"
)

func openTest(t *testing.T, now time.Time) *Archive {
	t.Helper()
	a, err := OpenArchive(filepath.Join(t.TempDir(), "calls.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func TestHeardBecomesTheSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE calls (
		id INTEGER PRIMARY KEY, number TEXT NOT NULL, direction TEXT NOT NULL, started_at TEXT NOT NULL,
		answered_at TEXT NOT NULL DEFAULT '', ended_at TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL);
		CREATE INDEX calls_by_start ON calls (started_at);
		ALTER TABLE calls ADD COLUMN heard TEXT NOT NULL DEFAULT '';
		INSERT INTO calls (number, direction, started_at, outcome, heard) VALUES ('+351911111111', 'outgoing', '2026-10-28T12:00:00Z', 'answered', 'forty-two');
		PRAGMA user_version = 2;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	a, err := OpenArchive(path, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	got, err := a.Calls(ctx, 10)
	if err != nil || len(got) != 1 || got[0].Summary != "forty-two" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestTurnsRoundTripInOrder(t *testing.T) {
	a := openTest(t, t0)
	id, _ := a.Start(ctx, "+351911111111", "outgoing", t0)
	want := []Turn{
		{Speaker: "box", Text: "What is 23 plus 19?", At: ts(t0)},
		{Speaker: "them", Text: "forty two", At: ts(t0.Add(time.Second))},
		{Speaker: "box", Text: "Thank you", At: ts(t0.Add(2 * time.Second))},
	}
	if err := a.AddTurns(ctx, id, want[:2]); err != nil {
		t.Fatal(err)
	}
	if err := a.AddTurns(ctx, id, want[2:]); err != nil {
		t.Fatal(err)
	}
	got, err := a.Turns(ctx, id)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
	calls, _ := a.Calls(ctx, 10)
	if !calls[0].HasTurns || calls[0].HasAudio {
		t.Fatalf("%+v", calls[0])
	}
}

func TestPurgeDebugKeepsTheLogRow(t *testing.T) {
	a := openTest(t, t0)
	old, _ := a.Start(ctx, "+351911111111", "outgoing", t0.AddDate(0, 0, -10))
	fresh, _ := a.Start(ctx, "+351922222222", "outgoing", t0.AddDate(0, 0, -1))
	for _, id := range []int64{old, fresh} {
		a.SetSummary(ctx, id, "forty two")
		a.AddTurns(ctx, id, []Turn{{Speaker: "them", Text: "hi", At: ts(t0)}})
		if err := os.WriteFile(a.AudioPath(id), []byte("RIFF"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.PurgeDebug(ctx, 0); err != nil { // 0 keeps everything
		t.Fatal(err)
	}
	if turns, _ := a.Turns(ctx, old); len(turns) != 1 {
		t.Fatal("days 0 must purge nothing")
	}
	if err := a.PurgeDebug(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if turns, _ := a.Turns(ctx, old); len(turns) != 0 {
		t.Fatalf("%+v", turns)
	}
	if _, err := os.Stat(a.AudioPath(old)); !os.IsNotExist(err) {
		t.Fatalf("old audio still there: %v", err)
	}
	calls, _ := a.Calls(ctx, 10)
	if len(calls) != 2 {
		t.Fatalf("%+v", calls)
	}
	for _, c := range calls {
		switch c.ID {
		case old:
			if c.Summary != "forty two" || c.HasTurns || c.HasAudio {
				t.Fatalf("%+v", c)
			}
		case fresh:
			if !c.HasTurns || !c.HasAudio {
				t.Fatalf("%+v", c)
			}
		}
	}
}

func TestPurgeSweepsAudioWithoutARow(t *testing.T) {
	a := openTest(t, t0)
	id, _ := a.Start(ctx, "+351911111111", "outgoing", t0)
	dir := filepath.Dir(a.AudioPath(id))
	keep := []string{a.AudioPath(id), filepath.Join(dir, "notes.txt"), filepath.Join(dir, "x.wav")}
	orphan := a.AudioPath(999)
	for _, p := range append(keep, orphan) {
		os.WriteFile(p, []byte("x"), 0o600)
	}
	if err := a.PurgeDebug(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan survived: %v", err)
	}
	for _, p := range keep {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
}

func TestPurgeAlsoDropsTurnsOfDeletedCalls(t *testing.T) {
	a := openTest(t, t0)
	id, _ := a.Start(ctx, "+351911111111", "outgoing", t0.AddDate(0, 0, -10))
	a.AddTurns(ctx, id, []Turn{{Speaker: "them", Text: "hi", At: ts(t0)}})
	if err := a.Purge(ctx, 7); err != nil {
		t.Fatal(err)
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d turns left", n)
	}
}

func TestTheAudioDirectoryIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "audio")
	if err := os.Mkdir(audio, 0o755); err != nil { // an older, looser directory is tightened
		t.Fatal(err)
	}
	a, err := OpenArchive(filepath.Join(dir, "calls.db"), func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	fi, err := os.Stat(audio)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("%v %v", fi, err)
	}
}

func TestWipeDropsTurnsAndAudioToo(t *testing.T) {
	a := openTest(t, t0)
	id, _ := a.Start(ctx, "+351911111111", "outgoing", t0)
	a.AddTurns(ctx, id, []Turn{{Speaker: "them", Text: "hi", At: ts(t0)}})
	os.WriteFile(a.AudioPath(id), []byte("RIFF"), 0o600)
	if err := a.Wipe(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&n)
	if _, err := os.Stat(a.AudioPath(id)); n != 0 || !os.IsNotExist(err) {
		t.Fatalf("%d turns, audio %v", n, err)
	}
}

func TestPurgeRemovesTheRecordingsOfDeletedCalls(t *testing.T) {
	a := openTest(t, t0)
	id, _ := a.Start(ctx, "+351911111111", "outgoing", t0.AddDate(0, 0, -10))
	os.WriteFile(a.AudioPath(id), []byte("RIFF"), 0o600)
	if err := a.Purge(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.AudioPath(id)); !os.IsNotExist(err) {
		t.Fatalf("recording survived Purge: %v", err)
	}
}

// Ids are reused once the newest rows are gone, so a turn kept for a call without a row would be
// inherited by the next call that gets its id.
func TestAddTurnsForACallWithoutARowKeepsNothing(t *testing.T) {
	a := openTest(t, t0)
	if err := a.AddTurns(ctx, 999, []Turn{{Speaker: "them", Text: "hi", At: ts(t0)}}); err != nil {
		t.Fatal(err)
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d turns kept for a call with no row", n)
	}
}

func TestPurgesDropStrayTurns(t *testing.T) {
	for name, purge := range map[string]func(*Archive) error{
		"PurgeDebug": func(a *Archive) error { return a.PurgeDebug(ctx, 7) },
		"Purge":      func(a *Archive) error { return a.Purge(ctx, 7) },
	} {
		t.Run(name, func(t *testing.T) {
			a := openTest(t, t0)
			id, _ := a.Start(ctx, "+351911111111", "outgoing", t0) // recent: its turn stays
			a.AddTurns(ctx, id, []Turn{{Speaker: "them", Text: "hi", At: ts(t0)}})
			if _, err := a.db.Exec(`INSERT INTO turns (call_id, seq, speaker, text, at) VALUES (999, 1, 'them', 'stray', ?)`, ts(t0)); err != nil {
				t.Fatal(err)
			}
			if err := purge(a); err != nil {
				t.Fatal(err)
			}
			var stray, kept int
			a.db.QueryRow(`SELECT COUNT(*) FROM turns WHERE call_id = 999`).Scan(&stray)
			a.db.QueryRow(`SELECT COUNT(*) FROM turns WHERE call_id = ?`, id).Scan(&kept)
			if stray != 0 || kept != 1 {
				t.Fatalf("stray %d (want 0), kept %d (want 1)", stray, kept)
			}
		})
	}
}

func TestUsageRoundTrips(t *testing.T) {
	a := openTest(t, t0)
	id, _ := a.Start(ctx, "+351911111111", "outgoing", t0)
	cost := 0.25
	want := &CallUsage{Call: ModelUsage{Tokens: realtime.Tokens{Model: "gpt-realtime-2.1", AudioIn: 1200, AudioOut: 800}, CostUSD: &cost},
		Transcription: ModelUsage{Tokens: realtime.Tokens{Model: "gpt-4o-transcribe", AudioIn: 100, TextOut: 8}}, PricesChecked: "2026-10-29"}
	if err := a.SetUsage(ctx, id, want); err != nil {
		t.Fatal(err)
	}
	calls, err := a.Calls(ctx, 10)
	if err != nil || len(calls) != 1 || !reflect.DeepEqual(calls[0].Usage, want) {
		t.Fatalf("%+v %v", calls, err)
	}
}

func TestResultAndPromptRoundTrip(t *testing.T) {
	a := openTest(t, t0)
	id, _ := a.Start(ctx, "+351911111111", "outgoing", t0)
	if err := a.SetResult(ctx, id, "done", "Book a table"); err != nil {
		t.Fatal(err)
	}
	calls, err := a.Calls(ctx, 10)
	if err != nil || len(calls) != 1 || calls[0].Result != "done" || calls[0].Prompt != "Book a table" {
		t.Fatalf("%+v %v", calls, err)
	}
}

func TestIncomingCallsCountUntilSeen(t *testing.T) {
	a := openTest(t, t0)
	a.Start(ctx, "+351911111111", "incoming", t0)
	a.Start(ctx, "+351922222222", "incoming", t0)
	a.Start(ctx, "+351933333333", "outgoing", t0)
	if n, err := a.Unseen(ctx); err != nil || n != 2 {
		t.Fatalf("unseen %d, %v", n, err)
	}
	if err := a.MarkSeen(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := a.Unseen(ctx); err != nil || n != 0 {
		t.Fatalf("unseen %d, %v", n, err)
	}
}

func TestOldCallsStartSeen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(strings.Join(migrations[:5], "\n") + `PRAGMA user_version = 5;
		INSERT INTO calls (number, direction, started_at, outcome) VALUES ('+351911111111', 'incoming', '2026-10-28T12:00:00Z', 'answered');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	a, err := OpenArchive(path, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if n, err := a.Unseen(ctx); err != nil || n != 0 {
		t.Fatalf("unseen %d, %v", n, err)
	}
}

func TestArchiveLive(t *testing.T) {
	a := openTest(t, t0)
	ctx := context.Background()
	ringing, _ := a.Start(ctx, "+351911111111", "outgoing", t0)
	talking, _ := a.Start(ctx, "+351922222222", "incoming", t0)
	if err := a.End(ctx, talking, t0, "in-progress"); err != nil {
		t.Fatal(err)
	}
	ended, _ := a.Start(ctx, "+351933333333", "outgoing", t0)
	if err := a.End(ctx, ended, t0, "answered"); err != nil {
		t.Fatal(err)
	}
	got, err := a.Live(ctx)
	want := []LiveCall{{ringing, "outgoing"}, {talking, "incoming"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v %v, want %v", got, err, want)
	}
}
