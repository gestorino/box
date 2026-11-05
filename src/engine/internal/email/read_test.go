package email

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

const ana = "Ana Silva <ana@example.com>"

// following is a rig whose service follows the account and is connected, with these archived:
// UIDs 1 and 2 in thread a@x, UID 3 in thread b@x, UID 4 sent by the owner in thread c@x.
func following(t *testing.T) *srig {
	t.Helper()
	g := newSrig(t)
	g.connect(t, "from_now", false)
	if err := g.s.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		uid               uint32
		from, id, refs, s string
	}{
		{1, ana, "a@x", "", "one"},
		{2, ana, "a2@x", "<a@x>", "two"},
		{3, ana, "b@x", "", "three"},
		{4, me, "c@x", "", "four"},
	} {
		if err := g.a.Save(ctx, me, m.uid, t0, rawMail(m.from, m.id, m.refs, m.s, m.s)); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func (g *srig) unread(t *testing.T) int {
	t.Helper()
	n, err := g.a.Unread(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (g *srig) reconcile(t *testing.T) {
	t.Helper()
	if err := g.s.reconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileSetsTheCount(t *testing.T) {
	g := following(t)
	g.gm.unread = []uint32{1, 2, 3}
	g.reconcile(t)
	if n := g.unread(t); n != 2 { // two UIDs in one thread, one in another
		t.Fatalf("%d", n)
	}
	call := g.gm.calls[0]
	if call.address != me || call.password != g.f.password || call.uidValidity != 1 || len(call.mark) != 0 {
		t.Fatalf("%+v", call)
	}
	threads, _ := g.a.Threads(ctx, me)
	got := map[string]int{}
	for _, th := range threads {
		got[th.Thread] = th.Unread
	}
	if got["a@x"] != 2 || got["b@x"] != 1 || got["c@x"] != 0 {
		t.Fatalf("%v", got)
	}
	g.gm.unread = []uint32{3} // read in Gmail meanwhile
	g.reconcile(t)
	if n := g.unread(t); n != 1 {
		t.Fatalf("%d", n)
	}
}

func TestUnreadMailNotArchivedDoesNotCount(t *testing.T) {
	g := following(t)
	g.gm.unread = []uint32{90, 91}
	g.reconcile(t)
	if n := g.unread(t); n != 0 {
		t.Fatalf("%d", n)
	}
}

func TestOutgoingNeverCounts(t *testing.T) {
	g := following(t)
	g.gm.unread = []uint32{4}
	g.reconcile(t)
	if n := g.unread(t); n != 0 {
		t.Fatalf("%d", n)
	}
}

func TestOpeningClearsAtOnceAndMarksNext(t *testing.T) {
	g := following(t)
	g.gm.unread = []uint32{1, 2, 3}
	g.reconcile(t)
	if err := g.s.MarkRead(ctx, "a@x"); err != nil {
		t.Fatal(err)
	}
	if n := g.unread(t); n != 1 {
		t.Fatalf("%d", n)
	}
	g.reconcile(t)
	if call := g.gm.calls[1]; !slices.Equal(call.mark, []uint32{1, 2}) {
		t.Fatalf("%+v", call)
	}
	if marks, _ := g.a.PendingMarks(ctx, me); len(marks) != 0 {
		t.Fatalf("%v", marks)
	}
	if n := g.unread(t); n != 1 {
		t.Fatalf("%d", n)
	}
	g.reconcile(t)
	if call := g.gm.calls[2]; len(call.mark) != 0 {
		t.Fatalf("marked twice: %+v", call)
	}
}

func TestOpeningAnUncountedThreadStillMarks(t *testing.T) {
	g := following(t)
	g.gm.unread = []uint32{1}
	g.reconcile(t)
	if err := g.s.MarkRead(ctx, "b@x"); err != nil { // never in the unread set (a Promotions thread, say)
		t.Fatal(err)
	}
	g.reconcile(t)
	if call := g.gm.calls[1]; !slices.Equal(call.mark, []uint32{3}) {
		t.Fatalf("%+v", call)
	}
}

func TestOpeningNeverMarksWhatTheOwnerSent(t *testing.T) {
	g := following(t)
	if err := g.s.MarkRead(ctx, "c@x"); err != nil {
		t.Fatal(err)
	}
	if marks, _ := g.a.PendingMarks(ctx, me); len(marks) != 0 {
		t.Fatalf("%v", marks)
	}
}

func TestAPendingRowStaysReadThroughAReconcile(t *testing.T) {
	g := following(t)
	g.gm.unread = []uint32{1, 2, 3}
	g.reconcile(t)
	// The owner opens the thread while a reconcile is on the wire: Gmail's answer is older than the mark.
	g.gm.during = func() {
		if err := g.s.MarkRead(ctx, "a@x"); err != nil {
			t.Error(err)
		}
	}
	g.reconcile(t)
	g.gm.during = nil
	if n := g.unread(t); n != 1 {
		t.Fatalf("%d", n)
	}
	if marks, _ := g.a.PendingMarks(ctx, me); !slices.Equal(marks, []uint32{1, 2}) {
		t.Fatalf("%v", marks)
	}
	g.reconcile(t) // the next call carries the mark
	if n := g.unread(t); n != 1 {
		t.Fatalf("%d", n)
	}
	if marks, _ := g.a.PendingMarks(ctx, me); len(marks) != 0 {
		t.Fatalf("%v", marks)
	}
}

func TestAMarkSurvivesARestart(t *testing.T) {
	a, c, path := openArchive(t)
	if err := a.Save(ctx, me, 5, t0, rawMail(ana, "r@x", "", "hi", "hi")); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkThreadRead(ctx, me, "r@x"); err != nil {
		t.Fatal(err)
	}
	a.Close()
	b, err := OpenArchive(path, c.now)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if marks, _ := b.PendingMarks(ctx, me); !slices.Equal(marks, []uint32{5}) {
		t.Fatalf("%v", marks)
	}
}

func TestAFailedReconcileKeepsTheCount(t *testing.T) {
	g := following(t)
	g.gm.unread = []uint32{1, 3}
	g.reconcile(t)
	before := g.s.Status()
	g.gm.err = errors.New("connection reset")
	g.gm.unread = nil
	if err := g.s.reconcileOnce(ctx); err == nil {
		t.Fatal("no error")
	}
	if n := g.unread(t); n != 2 {
		t.Fatalf("%d", n)
	}
	if st := g.s.Status(); st != before {
		t.Fatalf("%+v then %+v", before, st)
	}
}

func TestAFailedReconcileKeepsTheMarks(t *testing.T) {
	g := following(t)
	if err := g.s.MarkRead(ctx, "b@x"); err != nil {
		t.Fatal(err)
	}
	g.gm.err = errors.New("connection reset")
	g.s.reconcileOnce(ctx)
	if marks, _ := g.a.PendingMarks(ctx, me); !slices.Equal(marks, []uint32{3}) {
		t.Fatalf("%v", marks)
	}
}

func TestARefusedLoginIsLeftToTheArchive(t *testing.T) {
	g := following(t)
	g.gm.err = ErrAuth
	if err := g.s.reconcileOnce(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("%v", err)
	}
	if st := g.s.Status(); st.State != "connected" {
		t.Fatalf("%+v", st)
	}
}

func TestRenumberingMarksNothingAndHeals(t *testing.T) {
	g := following(t)
	if err := g.s.MarkRead(ctx, "b@x"); err != nil {
		t.Fatal(err)
	}
	g.gm.validity = 2 // the mailbox was renumbered
	g.gm.unread = []uint32{1}
	if err := g.s.reconcileOnce(ctx); !errors.Is(err, ErrRenumbered) {
		t.Fatalf("%v", err)
	}
	if marks, _ := g.a.PendingMarks(ctx, me); !slices.Equal(marks, []uint32{3}) {
		t.Fatalf("%v", marks)
	}
	if n := g.unread(t); n != 0 {
		t.Fatalf("%d", n)
	}
	// the archive copies the window again: the same raw email arrives under a new UID
	if err := g.a.Save(ctx, me, 30, t0, rawMail(ana, "b@x", "", "three", "three")); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.a.Count(ctx, me); n != 4 {
		t.Fatalf("%d rows", n)
	}
	if marks, _ := g.a.PendingMarks(ctx, me); !slices.Equal(marks, []uint32{30}) {
		t.Fatalf("%v", marks)
	}
}

func TestARenumberedMailboxNeverMarksByAStaleUID(t *testing.T) {
	g := following(t)
	g.gm.unread = []uint32{1, 2, 3}
	g.reconcile(t)
	for _, thread := range []string{"a@x", "b@x"} { // UIDs 1, 2 and 3 are pending
		if err := g.s.MarkRead(ctx, thread); err != nil {
			t.Fatal(err)
		}
	}
	g.f.uidValidity, g.gm.validity = 2, 2 // the server renumbers
	if err := g.s.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := g.unread(t); n != 0 {
		t.Fatalf("%d counted by stale UIDs", n)
	}
	g.gm.unread = []uint32{1, 2, 3} // the new numbering: these name other mail
	g.reconcile(t)
	if call := g.gm.calls[len(g.gm.calls)-1]; call.uidValidity != 2 || len(call.mark) != 0 {
		t.Fatalf("sent %+v", call)
	}
	if n := g.unread(t); n != 0 {
		t.Fatalf("%d counted by stale UIDs", n)
	}
	// the archive finds b@x again under its new UID: only it can be marked; a@x's rows have none yet
	if err := g.a.Save(ctx, me, 30, t0, rawMail(ana, "b@x", "", "three", "three")); err != nil {
		t.Fatal(err)
	}
	g.gm.unread = []uint32{30}
	g.reconcile(t)
	if call := g.gm.calls[len(g.gm.calls)-1]; !slices.Equal(call.mark, []uint32{30}) {
		t.Fatalf("sent %+v", call)
	}
	if marks, _ := g.a.PendingMarks(ctx, me); len(marks) != 0 {
		t.Fatalf("%v", marks)
	}
	var waiting int
	g.a.db.QueryRow(`SELECT COUNT(*) FROM emails WHERE mark_pending = 1 AND uid = 0`).Scan(&waiting)
	if waiting != 2 { // a@x's two emails keep their marks, unsent
		t.Fatalf("%d", waiting)
	}
}

func TestTheOldAccountsMarksStayBehind(t *testing.T) {
	g := following(t)
	const old = "old@gmail.com"
	if err := g.a.Save(ctx, old, 7, t0, rawMail(ana, "o@x", "", "old", "old")); err != nil {
		t.Fatal(err)
	}
	if err := g.a.MarkThreadRead(ctx, old, "o@x"); err != nil {
		t.Fatal(err)
	}
	if err := g.s.MarkRead(ctx, "o@x"); err != nil { // not a thread of the followed account
		t.Fatal(err)
	}
	g.reconcile(t)
	if call := g.gm.calls[0]; len(call.mark) != 0 {
		t.Fatalf("%+v", call)
	}
	if marks, _ := g.a.PendingMarks(ctx, old); !slices.Equal(marks, []uint32{7}) {
		t.Fatalf("%v", marks)
	}
}

func TestReconcileWaitsForAFollowedMailbox(t *testing.T) {
	g := newSrig(t)
	if err := g.s.reconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	g.connect(t, "from_now", false) // connecting: the archive has not yet logged in
	if err := g.s.reconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := g.gm.callCount(); n != 0 {
		t.Fatalf("%d calls", n)
	}
}

func TestMobilePace(t *testing.T) {
	g := newSrig(t)
	if d := g.s.reconcileWait(ctx); d != time.Minute {
		t.Fatalf("%v", d)
	}
	g.uplink = "mobile"
	if d := g.s.reconcileWait(ctx); d != 15*time.Minute {
		t.Fatalf("%v", d)
	}
	g.uplink, g.uplinkErr = "", errors.New("hostd down")
	if d := g.s.reconcileWait(ctx); d != time.Minute {
		t.Fatalf("%v", d)
	}
}

func TestOpeningWakesTheLoop(t *testing.T) {
	g := following(t)
	g.gm.called = make(chan struct{}, 8)
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	go g.s.reconcileLoop(c)
	waitCall(t, g.gm.called) // the first pass, at start
	if err := g.s.MarkRead(ctx, "b@x"); err != nil {
		t.Fatal(err)
	}
	waitCall(t, g.gm.called) // the nudge: not the minute
	if call := g.gm.calls[1]; !slices.Equal(call.mark, []uint32{3}) {
		t.Fatalf("%+v", call)
	}
}

func TestOpeningWaitsForTheMobilePace(t *testing.T) {
	g := following(t)
	g.uplink = "mobile"
	g.gm.called = make(chan struct{}, 8)
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	go g.s.reconcileLoop(c)
	waitCall(t, g.gm.called)
	if err := g.s.MarkRead(ctx, "b@x"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.gm.called:
		t.Fatal("reconciled on mobile data at an open")
	case <-time.After(200 * time.Millisecond):
	}
	if marks, _ := g.a.PendingMarks(ctx, me); !slices.Equal(marks, []uint32{3}) {
		t.Fatalf("%v", marks)
	}
}

func TestMigratedRowsStartRead(t *testing.T) {
	a, c, path := openArchive(t)
	if err := a.Save(ctx, me, 1, t0, rawMail(ana, "m@x", "", "hi", "hi")); err != nil {
		t.Fatal(err)
	}
	a.Close()
	// as an older build left it: no read state, user_version 1
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`ALTER TABLE accounts ADD COLUMN app_password TEXT NOT NULL DEFAULT ''`, `DROP INDEX emails_stored`, `ALTER TABLE emails DROP COLUMN stored_at`, `DROP INDEX emails_unread`, `DROP INDEX emails_uid`, `DROP INDEX emails_pending`, `ALTER TABLE emails DROP COLUMN unread`,
		`ALTER TABLE emails DROP COLUMN mark_pending`, `PRAGMA user_version = 1`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()
	b, err := OpenArchive(path, c.now)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if n, err := b.Unread(ctx, me); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	if n, _ := b.Count(ctx, me); n != 1 {
		t.Fatalf("%d rows", n)
	}
}

func TestTheAppPasswordColumnGoesAndAccountsStay(t *testing.T) {
	a, c, path := openArchive(t)
	a.Close()
	// as the previous build left it: user_version 3, the old app_password column
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`ALTER TABLE accounts ADD COLUMN app_password TEXT NOT NULL DEFAULT ''`,
		`INSERT INTO accounts (address, state, connected_at, app_password) VALUES ('me@x.test', 'connected', '2026-11-03T10:00:00Z', 'old')`,
		`PRAGMA user_version = 3`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()
	b, err := OpenArchive(path, c.now)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var n int
	var state string
	b.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('accounts') WHERE name = 'app_password'`).Scan(&n)
	b.db.QueryRow(`SELECT state FROM accounts WHERE address = 'me@x.test'`).Scan(&state)
	if n != 0 || state != "connected" {
		t.Fatalf("app_password columns = %d, state = %q", n, state)
	}
}

func TestUnreadAndReadEndpoints(t *testing.T) {
	g := following(t)
	h := NewHandler(g.s, g.a, g.ls())
	g.gm.unread = []uint32{1, 3}
	g.reconcile(t)
	if rec := call(h, "GET", "/email/unread", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"count":2}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/email/read", `{"thread":"b@x"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "GET", "/email/unread", ""); strings.TrimSpace(rec.Body.String()) != `{"count":1}` {
		t.Fatalf("%s", rec.Body)
	}
	for _, body := range []string{`{}`, `{"thread":""}`, `{"thread":"b@x","x":1}`, `nope`} {
		if rec := call(h, "POST", "/email/read", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, rec.Code)
		}
	}
}

func waitCall(t *testing.T, called chan struct{}) {
	t.Helper()
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("no reconcile")
	}
}

func TestUnreadIsZeroWithoutAnAccount(t *testing.T) {
	g := newSrig(t)
	rec := call(NewHandler(g.s, g.a, g.ls()), "GET", "/email/unread", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"count":0}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(NewHandler(g.s, g.a, g.ls()), "POST", "/email/read", `{"thread":"b@x"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d", rec.Code)
	}
}

// changes is how many rows the connection has written so far (the archive has one connection).
func changes(t *testing.T, a *Archive) int {
	t.Helper()
	var n int
	if err := a.db.QueryRow(`SELECT total_changes()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestARepeatedReconcileWritesNothing(t *testing.T) {
	g := following(t)
	g.gm.unread = []uint32{1, 2, 3}
	g.reconcile(t)
	before := changes(t, g.a)
	g.reconcile(t)
	g.reconcile(t)
	if after := changes(t, g.a); after != before {
		t.Fatalf("an unchanged reconcile wrote %d rows", after-before)
	}
	if n := g.unread(t); n != 2 {
		t.Fatalf("%d", n)
	}
}

func TestAJustMarkedRowStaysReadWhileGmailStillListsIt(t *testing.T) {
	g := following(t)
	if err := g.a.ApplyReconcile(ctx, me, nil, []uint32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := g.s.MarkRead(ctx, "b@x"); err != nil { // UID 3, pending
		t.Fatal(err)
	}
	// Gmail's search lags the STORE: it carries 3 as marked and still lists it as unread.
	if err := g.a.ApplyReconcile(ctx, me, []uint32{3}, []uint32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if n := g.unread(t); n != 1 {
		t.Fatalf("%d, want the one thread still unread", n)
	}
	if marks, _ := g.a.PendingMarks(ctx, me); len(marks) != 0 {
		t.Fatalf("%v", marks)
	}
	// A later round that still lists it counts it again.
	if err := g.a.ApplyReconcile(ctx, me, nil, []uint32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if n := g.unread(t); n != 2 {
		t.Fatalf("%d", n)
	}
}

func TestPendingMarksUsesThePartialIndex(t *testing.T) {
	g := following(t)
	var plan string
	rows, err := g.a.db.Query(`EXPLAIN QUERY PLAN `+pendingMarksSQL, me)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "emails_pending") {
		t.Fatalf("plan:\n%s", plan)
	}
}

func TestARefusedLoginWaitsFifteenMinutesAndIgnoresNudges(t *testing.T) {
	g := newSrig(t)
	if d, nudgeable := g.s.nextWait(ctx, ErrAuth); d != reconcileAuth || nudgeable {
		t.Fatalf("%v %v", d, nudgeable)
	}
	if d, nudgeable := g.s.nextWait(ctx, fmt.Errorf("reconcile: %w", ErrAuth)); d != reconcileAuth || nudgeable {
		t.Fatalf("wrapped: %v %v", d, nudgeable)
	}
	if d, nudgeable := g.s.nextWait(ctx, errors.New("timeout")); d != reconcileEvery || !nudgeable {
		t.Fatalf("other error: %v %v", d, nudgeable)
	}
	if d, nudgeable := g.s.nextWait(ctx, nil); d != reconcileEvery || !nudgeable {
		t.Fatalf("no error: %v %v", d, nudgeable)
	}
	g.uplink = "mobile"
	if d, _ := g.s.nextWait(ctx, ErrAuth); d != reconcileAuth {
		t.Fatalf("%v", d)
	}
}
