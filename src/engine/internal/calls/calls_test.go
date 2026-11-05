package calls

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var ctx = context.Background()
var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

type fakeModem struct {
	mu        sync.Mutex
	instance  string
	calls     []ModemCall
	deleted   []string
	deleteErr error
}

func (f *fakeModem) List(context.Context) (string, []ModemCall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.instance, append([]ModemCall(nil), f.calls...), nil
}

func (f *fakeModem) Delete(_ context.Context, instance, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if instance != f.instance {
		return ErrModemChanged
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, path)
	kept := f.calls[:0]
	for _, c := range f.calls {
		if c.Path != path {
			kept = append(kept, c)
		}
	}
	f.calls = kept
	return nil
}

func (f *fakeModem) Dial(context.Context, string) (string, string, error) {
	return "", "", errors.New("not a dialling fake")
}
func (f *fakeModem) Hangup(context.Context, string, string) error { return nil }

func (f *fakeModem) set(state, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[0].State, f.calls[0].Reason = state, reason
}

type rig struct {
	m         *fakeModem
	a         *Archive
	r         *Archiver
	now       time.Time
	days      int
	debugDays int
	known     bool // hostd has answered
}

func newRig(t *testing.T) *rig {
	t.Helper()
	g := &rig{m: &fakeModem{instance: ":1.10"}, now: t0, days: 365, debugDays: 30, known: true}
	a, err := OpenArchive(filepath.Join(t.TempDir(), "calls.db"), func() time.Time { return g.now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	g.a = a
	g.r = NewArchiver(g.m, a, func() (int, bool) { return g.days, g.known }, func() (int, bool) { return g.debugDays, g.known }, func() time.Time { return g.now })
	return g
}

func (g *rig) tickAt(d time.Duration) { g.now = t0.Add(d); g.r.Tick(ctx) }

func TestCallOutcomes(t *testing.T) {
	for _, c := range []struct {
		dir      string
		answered bool
		reason   string
		want     string
	}{
		{"incoming", true, "terminated", "answered"},
		{"incoming", false, "refused-or-busy", "declined"},
		{"incoming", false, "terminated", "missed"},
		{"outgoing", true, "terminated", "answered"},
		{"outgoing", false, "refused-or-busy", "busy"},
		{"outgoing", false, "error", "failed"},
		{"outgoing", false, "audio-setup-failed", "failed"},
		{"outgoing", false, "terminated", "no-answer"},
	} {
		if got := outcome(c.dir, c.answered, c.reason); got != c.want {
			t.Errorf("%s answered=%t %s: got %s, want %s", c.dir, c.answered, c.reason, got, c.want)
		}
	}
}

func TestCallTimesAndDelete(t *testing.T) {
	g := newRig(t)
	g.m.calls = []ModemCall{{Path: "/Call/1", Number: "+351911111111", Direction: "incoming", State: "ringing-in"}}
	g.tickAt(0)
	g.m.set("active", "accepted")
	g.tickAt(4 * time.Second)
	g.m.set("terminated", "terminated")
	g.tickAt(64 * time.Second)
	calls, _ := g.a.Calls(ctx, 500)
	want := CallRecord{ID: calls[0].ID, Number: "+351911111111", Direction: "incoming", StartedAt: "2026-10-28T12:00:00Z",
		AnsweredAt: "2026-10-28T12:00:04Z", EndedAt: "2026-10-28T12:01:04Z", Outcome: "answered"}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("got %+v", calls)
	}
	if len(g.m.deleted) != 1 {
		t.Fatalf("not deleted from ModemManager: %v", g.m.deleted)
	}
}

func TestLiveOutcomeWhileRinging(t *testing.T) {
	g := newRig(t)
	g.m.calls = []ModemCall{{Path: "/Call/1", Number: "X", Direction: "incoming", State: "ringing-in"}}
	g.tickAt(0)
	if calls, _ := g.a.Calls(ctx, 500); calls[0].Outcome != "ringing" || calls[0].EndedAt != "" {
		t.Fatalf("%+v", calls)
	}
}

func TestVanishedCallClosedUnknown(t *testing.T) {
	g := newRig(t)
	g.m.calls = []ModemCall{{Path: "/Call/1", Number: "X", Direction: "incoming", State: "ringing-in"}}
	g.tickAt(0)
	g.tickAt(2 * time.Second)
	g.m.calls = nil // ModemManager restarted mid-call
	g.tickAt(4 * time.Second)
	calls, _ := g.a.Calls(ctx, 500)
	if calls[0].Outcome != "unknown" || calls[0].EndedAt != "2026-10-28T12:00:02Z" {
		t.Fatalf("%+v (closed at the last time it was seen)", calls)
	}
}

func TestCloseLiveAtStart(t *testing.T) {
	g := newRig(t)
	id, _ := g.a.Start(ctx, "X", "incoming", t0)
	done, _ := g.a.Start(ctx, "Y", "outgoing", t0)
	g.a.End(ctx, done, t0.Add(time.Minute), "answered")
	g.a.CloseLive(ctx, t0.Add(time.Hour))
	calls, _ := g.a.Calls(ctx, 500)
	for _, c := range calls {
		if c.ID == id && (c.Outcome != "unknown" || c.EndedAt != "2026-10-28T13:00:00Z") {
			t.Fatalf("live row not closed: %+v", c)
		}
		if c.ID == done && c.Outcome != "answered" {
			t.Fatalf("finished row touched: %+v", c)
		}
	}
}

func TestFinishedCallFoundRecordedOnce(t *testing.T) {
	g := newRig(t)
	g.m.calls = []ModemCall{{Path: "/Call/3", Number: "912345678", Direction: "incoming", State: "terminated", Reason: "terminated"}}
	g.m.deleteErr = errors.New("busy")
	g.tickAt(0)
	g.tickAt(2 * time.Second)
	g.m.deleteErr = nil
	g.tickAt(4 * time.Second)
	calls, _ := g.a.Calls(ctx, 500)
	if len(calls) != 1 || calls[0].Outcome != "unknown" || len(g.m.deleted) != 1 {
		t.Fatalf("calls %+v deleted %v", calls, g.m.deleted)
	}
}

func TestModemPathChangePickedUp(t *testing.T) {
	g := newRig(t)
	g.m.calls = []ModemCall{{Path: "/Call/1", Number: "X", Direction: "incoming", State: "ringing-in"}}
	g.tickAt(0)
	g.m.instance = ":1.42"
	g.m.calls = []ModemCall{{Path: "/Call/1", Number: "Y", Direction: "outgoing", State: "dialing"}}
	g.tickAt(2 * time.Second)
	calls, _ := g.a.Calls(ctx, 500)
	if len(calls) != 2 || calls[0].Number != "Y" || calls[1].Outcome != "unknown" {
		t.Fatalf("%+v", calls)
	}
}

func TestPurgeByStartedAt(t *testing.T) {
	g := newRig(t)
	g.days = 30
	g.a.Start(ctx, "X", "incoming", t0)
	g.tickAt(31 * 24 * time.Hour)
	if calls, _ := g.a.Calls(ctx, 500); len(calls) != 0 {
		t.Fatalf("not purged: %+v", calls)
	}
}

func TestNoPurgeUntilRetentionKnown(t *testing.T) {
	g := newRig(t)
	g.a.Start(ctx, "X", "incoming", t0)
	g.known = false // calld restarted on a box set to keep calls forever; hostd has not answered
	g.tickAt(400 * 24 * time.Hour)
	if calls, _ := g.a.Calls(ctx, 500); len(calls) != 1 {
		t.Fatalf("purged before the retention was known: %+v", calls)
	}
	g.known, g.days = true, 30
	g.tickAt(400*24*time.Hour + time.Second)
	if calls, _ := g.a.Calls(ctx, 500); len(calls) != 0 {
		t.Fatalf("30 days must purge it: %+v", calls)
	}
}

func TestHandlerNewestFirst(t *testing.T) {
	g := newRig(t)
	g.a.Start(ctx, "old", "incoming", t0)
	g.a.Start(ctx, "new", "outgoing", t0.Add(time.Hour))
	h := NewHandler(ctx, g.a, nil, noKey(t), voiceUnknown, ownerUnknown)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/calls?limit=500", nil))
	var calls []CallRecord
	json.Unmarshal(rec.Body.Bytes(), &calls)
	if len(calls) != 2 || calls[0].Number != "new" {
		t.Fatalf("%s", rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/calls?limit=0", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("limit 0: %d", rec.Code)
	}
}

func postJSON(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec
}

func removeCalls(h http.Handler, body string) int { return postJSON(h, "/calls/remove", body).Code }

func TestRemoveKeepLeavesArchive(t *testing.T) {
	g := newRig(t)
	g.a.Start(ctx, "+351911111111", "incoming", t0)
	if code := removeCalls(NewHandler(ctx, g.a, nil, noKey(t), voiceUnknown, ownerUnknown), `{"delete_data":false,"force":false}`); code != http.StatusNoContent {
		t.Fatalf("%d", code)
	}
	if calls, _ := g.a.Calls(ctx, 10); len(calls) != 1 {
		t.Fatalf("kept archive lost: %+v", calls)
	}
}

func TestRemoveDeleteWipes(t *testing.T) {
	g := newRig(t)
	g.a.Start(ctx, "+351911111111", "incoming", t0)
	g.a.Start(ctx, "+351922222222", "outgoing", t0)
	h := NewHandler(ctx, g.a, nil, noKey(t), voiceUnknown, ownerUnknown)
	for range 2 {
		if code := removeCalls(h, `{"delete_data":true,"force":false}`); code != http.StatusNoContent {
			t.Fatalf("%d", code)
		}
	}
	if calls, _ := g.a.Calls(ctx, 10); len(calls) != 0 {
		t.Fatalf("not wiped: %+v", calls)
	}
	if code := removeCalls(h, `{"delete_data":1}`); code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", code)
	}
}

// The Caller holds the call it placed: the archiver records its end but leaves it in ModemManager
// until released, so the Caller can still read how it ended.
func TestHeldCallIsNotClearedUntilReleased(t *testing.T) {
	g := newRig(t)
	g.m.calls = []ModemCall{{Path: "/Call/1", Number: "+351911111111", Direction: "outgoing", State: "ringing-out"}}
	release := g.r.Hold("/Call/1")
	g.tickAt(0)
	g.m.set("terminated", "refused-or-busy")
	g.tickAt(2 * time.Second)
	calls, _ := g.a.Calls(ctx, 10)
	if len(calls) != 1 || calls[0].Outcome != "busy" {
		t.Fatalf("%+v", calls)
	}
	if len(g.m.deleted) != 0 {
		t.Fatalf("cleared while held: %v", g.m.deleted)
	}
	release()
	g.tickAt(4 * time.Second)
	if len(g.m.deleted) != 1 || g.m.deleted[0] != "/Call/1" {
		t.Fatalf("not cleared once released: %v", g.m.deleted)
	}
	if calls, _ := g.a.Calls(ctx, 10); len(calls) != 1 {
		t.Fatalf("recorded twice: %+v", calls)
	}
}
