package calls

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"concierge/engine/internal/realtime"
)

// tmpAudio is a recording as the Caller leaves it: a file in its own temp directory.
func tmpAudio(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "call-*.wav")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("RIFF")
	f.Close()
	return f.Name()
}

func summaryOf(t *testing.T, g *rig) string {
	t.Helper()
	calls, err := g.a.Calls(ctx, 10)
	if err != nil || len(calls) != 1 {
		t.Fatalf("%+v %v", calls, err)
	}
	return calls[0].Summary
}

// What the box kept on its own call lands on that call's row, whether it comes while the call is
// recorded as live or before the archiver first saw it.
func TestTheSummaryIsStoredOnTheCall(t *testing.T) {
	g := newRig(t)
	release := g.r.Hold("/Call/1")
	g.m.calls = []ModemCall{{Path: "/Call/1", Number: "+351912345678", Direction: "outgoing", State: "active"}}
	g.tickAt(0)
	g.r.Keep("/Call/1", Kept{Summary: "forty two"})
	g.m.calls[0].State, g.m.calls[0].Reason = "terminated", "terminated"
	g.tickAt(2 * time.Second)
	release()
	if s := summaryOf(t, g); s != "forty two" {
		t.Fatalf("%q", s)
	}
}

func TestTheSummaryKeptBeforeTheCallWasSeen(t *testing.T) {
	g := newRig(t)
	release := g.r.Hold("/Call/1")
	g.r.Keep("/Call/1", Kept{Summary: "forty two"})
	g.m.calls = []ModemCall{{Path: "/Call/1", Number: "+351912345678", Direction: "outgoing", State: "terminated", Reason: "terminated"}}
	g.tickAt(0)
	release()
	if s := summaryOf(t, g); s != "forty two" {
		t.Fatalf("%q", s)
	}
}

func TestKeptTurnsAndAudioLandOnTheRow(t *testing.T) {
	g := newRig(t)
	release := g.r.Hold("/Call/1")
	audio := tmpAudio(t)
	turns := []Turn{{Speaker: "box", Text: "What is 23 plus 19?", At: ts(t0)}, {Speaker: "them", Text: "forty two", At: ts(t0)}}
	g.r.Keep("/Call/1", Kept{Summary: "s", Turns: turns, Audio: audio}) // before the archiver saw the call
	g.m.calls = []ModemCall{{Path: "/Call/1", Number: "+351912345678", Direction: "outgoing", State: "terminated", Reason: "terminated"}}
	g.tickAt(0)
	release()
	calls, _ := g.a.Calls(ctx, 10)
	if len(calls) != 1 || calls[0].Summary != "s" || !calls[0].HasTurns || !calls[0].HasAudio {
		t.Fatalf("%+v", calls)
	}
	if got, _ := g.a.Turns(ctx, calls[0].ID); !reflect.DeepEqual(got, turns) {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(audio); !os.IsNotExist(err) {
		t.Fatalf("the temp file is still there: %v", err)
	}
}

func TestKeptAudioWithoutARowIsRemoved(t *testing.T) {
	g := newRig(t)
	release := g.r.Hold("/Call/1")
	audio := tmpAudio(t)
	g.r.Keep("/Call/1", Kept{Summary: "s", Audio: audio})
	release() // released before any tick recorded it: no row will ever take it
	if _, err := os.Stat(audio); !os.IsNotExist(err) {
		t.Fatalf("the temp file is still there: %v", err)
	}
}

// The tick purges transcripts and audio by their own retention, keeping the log rows, and not before
// hostd has answered.
func TestTheTickPurgesDebugByItsOwnRetention(t *testing.T) {
	g := newRig(t)
	id, _ := g.a.Start(ctx, "X", "outgoing", t0)
	g.a.AddTurns(ctx, id, []Turn{{Speaker: "them", Text: "hi", At: ts(t0)}})
	g.known = false
	g.tickAt(31 * 24 * time.Hour)
	if calls, _ := g.a.Calls(ctx, 10); len(calls) != 1 || !calls[0].HasTurns {
		t.Fatalf("purged before the debug retention was known: %+v", calls)
	}
	g.known = true
	g.tickAt(31*24*time.Hour + time.Second)
	if calls, _ := g.a.Calls(ctx, 10); len(calls) != 1 || calls[0].HasTurns {
		t.Fatalf("30 debug days must drop the turns and keep the row: %+v", calls)
	}
}

func TestCallsArchiveMigratesV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE calls (
		id INTEGER PRIMARY KEY, number TEXT NOT NULL, direction TEXT NOT NULL, started_at TEXT NOT NULL,
		answered_at TEXT NOT NULL DEFAULT '', ended_at TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL);
		CREATE INDEX calls_by_start ON calls (started_at);
		INSERT INTO calls (number, direction, started_at, outcome) VALUES ('+351911111111', 'incoming', '2026-10-28T12:00:00Z', 'missed');
		PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	a, err := OpenArchive(path, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	calls, err := a.Calls(ctx, 10)
	if err != nil || len(calls) != 1 || calls[0].Outcome != "missed" || calls[0].Summary != "" {
		t.Fatalf("%+v %v", calls, err)
	}
	var v int
	a.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != len(migrations) {
		t.Fatalf("user_version %d", v)
	}
	a.Close()
	if again, err := OpenArchive(path, func() time.Time { return t0 }); err != nil {
		t.Fatal(err)
	} else {
		again.Close()
	}
}

func TestKeptUsageLandsOnTheRow(t *testing.T) {
	g := newRig(t)
	release := g.r.Hold("/Call/1")
	u := costed(realtime.Usage{Call: realtime.Tokens{Model: "gpt-realtime-2.1", AudioIn: 1000}})
	g.r.Keep("/Call/1", Kept{Usage: u})
	g.m.calls = []ModemCall{{Path: "/Call/1", Number: "+351912345678", Direction: "outgoing", State: "terminated", Reason: "terminated"}}
	g.tickAt(0)
	release()
	calls, err := g.a.Calls(ctx, 10)
	if err != nil || len(calls) != 1 || !reflect.DeepEqual(calls[0].Usage, u) {
		t.Fatalf("%+v %v", calls, err)
	}
}

func TestKeptResultLandsOnTheRow(t *testing.T) {
	g := newRig(t)
	release := g.r.Hold("/Call/1")
	g.m.calls = []ModemCall{{Path: "/Call/1", Number: "+351912345678", Direction: "outgoing", State: "active"}}
	g.tickAt(0)
	g.r.Keep("/Call/1", Kept{Result: "done", Prompt: "Book a table"})
	g.m.calls[0].State, g.m.calls[0].Reason = "terminated", "terminated"
	g.tickAt(2 * time.Second)
	release()
	calls, err := g.a.Calls(ctx, 10)
	if err != nil || len(calls) != 1 || calls[0].Result != "done" || calls[0].Prompt != "Book a table" {
		t.Fatalf("%+v %v", calls, err)
	}
}
