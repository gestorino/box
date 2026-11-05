package email

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"concierge/engine/internal/secrets"
	"concierge/engine/internal/secrets/secretstest"
)

type srig struct {
	f         *fakeServer
	gm        *fakeGmail
	a         *Archive
	c         *clock
	s         *Service
	days      int
	known     bool
	uplink    string
	uplinkErr error
	sent      []sentMail
	sendErr   error
	sec       *secrets.Client
}

type sentMail struct {
	address, password string
	m                 Outgoing
}

func (g *srig) send(_ context.Context, address, password string, m Outgoing) error {
	if g.sendErr != nil {
		return g.sendErr
	}
	g.sent = append(g.sent, sentMail{address, password, m})
	return nil
}

func newSrig(t *testing.T) *srig {
	t.Helper()
	g := &srig{f: newFakeServer(), gm: newFakeGmail(), days: 365, known: true, uplink: "wifi"}
	g.a, g.c, _ = openArchive(t)
	g.sec = secretstest.Start(t, "email")
	g.s = g.service()
	return g
}

func (g *srig) service() *Service { return g.serviceWith(g.sec) }

func (g *srig) serviceWith(sec *secrets.Client) *Service {
	return NewService(g.f.dial, g.send, g.gm.reconcile, sec, g.a, func() (int, bool) { return g.days, g.known },
		func(context.Context) (string, error) { return g.uplink, g.uplinkErr }, g.c.now)
}

func (g *srig) connect(t *testing.T, history string, copyOnMobile bool) {
	t.Helper()
	if err := g.s.Connect(ctx, me, g.f.password, history, copyOnMobile); err != nil {
		t.Fatal(err)
	}
}

func (g *srig) count(t *testing.T) int {
	t.Helper()
	n, err := g.a.Count(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (g *srig) mail(daysAgo int, subject string) uint32 {
	return g.f.add(g.c.t.AddDate(0, 0, -daysAgo), rawMail("Ana Silva <ana@example.com>", subject+"@x", "", subject, subject))
}

func TestCopyWithinRetention(t *testing.T) {
	g := newSrig(t)
	g.mail(400, "old")
	g.mail(10, "recent")
	g.connect(t, "copy", false)
	if err := g.s.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	threads, _ := g.a.Threads(ctx, me)
	if len(threads) != 1 || threads[0].Thread != "recent@x" || g.s.Status().State != "connected" {
		t.Fatalf("%+v %+v", threads, g.s.Status())
	}
}

func TestCopyForeverTakesAll(t *testing.T) {
	g := newSrig(t)
	g.days = 0
	g.mail(4000, "ancient")
	g.mail(10, "recent")
	g.connect(t, "copy", false)
	g.s.syncOnce(ctx)
	if n := g.count(t); n != 2 {
		t.Fatalf("%d stored", n)
	}
}

func TestFromNowSkipsExisting(t *testing.T) {
	g := newSrig(t)
	g.mail(1, "before")
	g.connect(t, "from_now", false)
	g.mail(0, "after")
	g.s.syncOnce(ctx)
	threads, _ := g.a.Threads(ctx, me)
	if len(threads) != 1 || threads[0].Thread != "after@x" {
		t.Fatalf("%+v", threads)
	}
}

func TestDraftsSkippedCursorAdvances(t *testing.T) {
	g := newSrig(t)
	g.mail(1, "kept")
	g.f.add(g.c.t, rawMail(me, "draft@x", "", "Draft", "half-written"))
	g.f.msgs[1].Draft = true
	g.connect(t, "copy", false)
	g.s.syncOnce(ctx)
	acct, _ := g.a.Current(ctx)
	if n := g.count(t); n != 1 || acct.Copying() || g.s.Status().State != "connected" {
		t.Fatalf("%d stored, cursor %+v, %+v", n, acct.Cursor, g.s.Status())
	}
}

func TestResumeAfterDropNoGapNoDuplicate(t *testing.T) {
	g := newSrig(t)
	for i := range 25 {
		g.mail(1, fmt.Sprintf("m%d", i))
	}
	g.connect(t, "copy", false)
	g.f.failFetchAt = 2 // the second batch of ten
	if err := g.s.syncOnce(ctx); err == nil {
		t.Fatal("the dropped connection must surface")
	}
	if n := g.count(t); n != 10 {
		t.Fatalf("%d stored before the drop", n)
	}
	if err := g.s.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := g.count(t); n != 25 || g.f.dials != 2 {
		t.Fatalf("%d stored, %d dials", n, g.f.dials)
	}
}

func TestUIDValidityChangeRecopiesOnce(t *testing.T) {
	g := newSrig(t)
	g.mail(1, "a")
	g.mail(1, "b")
	g.connect(t, "copy", false)
	g.s.syncOnce(ctx)
	g.f.uidValidity = 2
	g.s.syncOnce(ctx)
	acct, _ := g.a.Current(ctx)
	if n := g.count(t); n != 2 || acct.UIDValidity != 2 || acct.Copying() {
		t.Fatalf("%d stored, %+v", n, acct.Cursor)
	}
}

func TestAuthFailedStopsRetrying(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	g.mail(0, "new")
	g.f.failFetchAt = 1 // the connection drops…
	g.s.syncOnce(ctx)
	g.f.password = "revoked" // …and the owner revokes the app password
	if err := g.s.syncOnce(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("%v", err)
	}
	dials := g.f.dials
	g.s.syncOnce(ctx)
	acct, _ := g.a.Current(ctx)
	if g.f.dials != dials || g.s.Status().State != "auth_failed" || acct.State != "auth_failed" {
		t.Fatalf("dials %d→%d, %+v, stored %q", dials, g.f.dials, g.s.Status(), acct.State)
	}
	// a restarted emaild stays put too
	g.s = g.service()
	g.s.resume(ctx)
	g.s.syncOnce(ctx)
	if g.f.dials != dials || g.s.Status().State != "auth_failed" || g.s.Status().Address != me {
		t.Fatalf("after restart: %d dials, %+v", g.f.dials, g.s.Status())
	}
}

func TestCopyPausesOnMobileNewMailArrives(t *testing.T) {
	g := newSrig(t)
	g.mail(3, "a")
	g.mail(2, "b")
	g.uplink = "mobile"
	g.connect(t, "copy", false)
	g.mail(0, "new")
	g.s.syncOnce(ctx)
	if n := g.count(t); n != 1 || g.s.Status() != (Status{State: "paused_mobile", Address: me, Copied: 1}) {
		t.Fatalf("%d stored, %+v", n, g.s.Status())
	}
	g.uplink, g.uplinkErr = "", errors.New("hostd away")
	g.s.syncOnce(ctx)
	if n := g.count(t); n != 1 || g.s.Status().State != "paused_mobile" {
		t.Fatalf("hostd unanswered must wait: %d, %+v", n, g.s.Status())
	}
	g.uplink, g.uplinkErr = "wifi", nil
	g.s.syncOnce(ctx)
	if n := g.count(t); n != 3 || g.s.Status().State != "connected" {
		t.Fatalf("%d stored, %+v", n, g.s.Status())
	}
}

func TestCopyOnMobileWhenAllowed(t *testing.T) {
	g := newSrig(t)
	g.mail(3, "a")
	g.uplink = "mobile"
	g.connect(t, "copy", true)
	g.s.syncOnce(ctx)
	if n := g.count(t); n != 1 {
		t.Fatalf("%d stored", n)
	}
}

func TestCopyWaitsForRetention(t *testing.T) {
	g := newSrig(t)
	g.mail(3, "a")
	g.mail(500, "b")
	g.known = false
	g.connect(t, "copy", false)
	g.s.syncOnce(ctx)
	if n := g.count(t); n != 0 || g.s.Status().State != "copying" {
		t.Fatalf("%d stored, %+v", n, g.s.Status())
	}
	g.known, g.days = true, 0
	g.s.syncOnce(ctx)
	if n := g.count(t); n != 2 {
		t.Fatalf("%d stored", n)
	}
}

func TestNoPurgeUntilRetentionKnown(t *testing.T) {
	g := newSrig(t)
	g.a.Save(ctx, me, 1, g.c.t.AddDate(0, 0, -400), rawMail("a@example.com", "old@x", "", "Old", "old"))
	g.known = false
	g.s.syncOnce(ctx) // nothing connected: purging is still its job
	if n := g.count(t); n != 1 {
		t.Fatal("purged before the retention was known")
	}
	g.known = true
	g.s.syncOnce(ctx)
	if n := g.count(t); n != 0 {
		t.Fatal("a year's retention must purge a 400-day-old email, connected or not")
	}
}

func TestPurgedNeverRecopied(t *testing.T) {
	g := newSrig(t)
	g.days = 0
	g.mail(400, "old")
	g.mail(10, "recent")
	g.connect(t, "copy", false)
	g.s.syncOnce(ctx)
	g.days = 365
	g.s.syncOnce(ctx)
	g.s.syncOnce(ctx)
	if n := g.count(t); n != 1 {
		t.Fatalf("%d stored", n)
	}
}

func TestReconnectResumes(t *testing.T) {
	g := newSrig(t)
	g.mail(3, "a")
	g.connect(t, "copy", false)
	g.s.syncOnce(ctx)
	if err := g.s.Disconnect(ctx, false); err != nil {
		t.Fatal(err)
	}
	if g.s.Status() != (Status{State: "disconnected", Address: me}) {
		t.Fatalf("%+v", g.s.Status())
	}
	g.mail(0, "while away")
	fetches := g.f.fetches
	g.connect(t, "from_now", false) // the choice is ignored: the cursor resumes
	g.s.syncOnce(ctx)
	if n := g.count(t); n != 2 || g.f.fetches != fetches+1 {
		t.Fatalf("%d stored, %d fetches", n, g.f.fetches-fetches)
	}
}

func TestConnectRefusedSavesNothing(t *testing.T) {
	g := newSrig(t)
	if err := g.s.Connect(ctx, me, "wrong", "copy", false); !errors.Is(err, ErrAuth) {
		t.Fatalf("%v", err)
	}
	if acct, _ := g.a.Current(ctx); acct != nil || g.s.Status().State != "disconnected" {
		t.Fatalf("%+v %+v", acct, g.s.Status())
	}
}

func TestConnectAndDisconnectStates(t *testing.T) {
	g := newSrig(t)
	if err := g.s.Disconnect(ctx, false); !errors.Is(err, ErrState) {
		t.Fatalf("disconnect while disconnected: %v", err)
	}
	g.connect(t, "from_now", false)
	if err := g.s.Connect(ctx, me, g.f.password, "copy", false); !errors.Is(err, ErrState) {
		t.Fatalf("connect while connected: %v", err)
	}
	if err := g.s.Disconnect(ctx, true); err != nil {
		t.Fatal(err)
	}
	if acct, _ := g.a.Current(ctx); acct != nil {
		t.Fatalf("%+v", acct)
	}
}

func TestLargeEmailStored(t *testing.T) {
	g := newSrig(t)
	big := bytes.Repeat([]byte("QUJD"), 25<<20/3) // 25 MB once decoded
	raw := []byte("From: a@example.com\r\nMessage-ID: <big@x>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nbig file\r\n--b\r\nContent-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=big.bin\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		strings.TrimSpace(string(big)) + "\r\n--b--\r\n")
	g.f.add(g.c.t, raw)
	g.connect(t, "copy", false)
	if err := g.s.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	emails, _ := g.a.Thread(ctx, me, "big@x")
	if len(emails) != 1 || len(emails[0].Attachments) != 1 {
		t.Fatalf("%+v", emails)
	}
	if got, _ := g.a.Raw(ctx, me, emails[0].ID); !bytes.Equal(got, raw) {
		t.Fatal("the original changed")
	}
}

func TestNewMailArrivesWhileTheCopyFails(t *testing.T) {
	g := newSrig(t)
	old := g.mail(3, "old")
	g.connect(t, "copy", false)
	g.mail(0, "new")
	g.f.failUIDs = map[uint32]bool{old: true} // Gmail's daily cap stops the copy…
	if err := g.s.syncOnce(ctx); err == nil {
		t.Fatal("the copy's failure must surface")
	}
	threads, _ := g.a.Threads(ctx, me)
	if len(threads) != 1 || threads[0].Thread != "new@x" { // …but not the new mail
		t.Fatalf("%+v", threads)
	}
}

func TestUplinkCheckedBeforeEachBatch(t *testing.T) {
	g := newSrig(t)
	for i := range 25 {
		g.mail(1, fmt.Sprintf("m%d", i))
	}
	g.connect(t, "copy", false)
	g.f.onFetch = func() { g.uplink = "mobile" } // Wi-Fi drops after the first batch
	g.s.syncOnce(ctx)
	if n := g.count(t); n != 10 || g.s.Status().State != "paused_mobile" {
		t.Fatalf("%d stored, %+v", n, g.s.Status())
	}
}

func TestDisconnectDuringDialClosesTheSession(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	g.mail(0, "new")
	g.f.failFetchAt = 1
	g.s.syncOnce(ctx) // the session drops; the next pass dials again
	g.f.dialBlock, g.f.dialing = make(chan struct{}), make(chan struct{}, 1)
	passed := make(chan error)
	go func() { passed <- g.s.syncOnce(ctx) }()
	<-g.f.dialing
	disconnected := make(chan error)
	go func() { disconnected <- g.s.Disconnect(ctx, true) }()
	time.Sleep(50 * time.Millisecond) // Disconnect is waiting for the pass
	close(g.f.dialBlock)
	<-passed
	if err := <-disconnected; err != nil {
		t.Fatal(err)
	}
	last := g.f.sessions[len(g.f.sessions)-1]
	g.f.mu.Lock()
	closed := last.closed
	g.f.mu.Unlock()
	if !closed || g.s.sess != nil || g.count(t) != 0 {
		t.Fatalf("a session dialled for a disconnected account stayed open (closed %v), %d stored", closed, g.count(t))
	}
}

func TestDisconnectKeepingEmailsDuringDialClosesTheSession(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	g.mail(0, "new")
	g.f.failFetchAt = 1
	g.s.syncOnce(ctx)
	g.f.dialBlock, g.f.dialing = make(chan struct{}), make(chan struct{}, 1)
	passed := make(chan error)
	go func() { passed <- g.s.syncOnce(ctx) }()
	<-g.f.dialing
	disconnected := make(chan error)
	go func() { disconnected <- g.s.Disconnect(ctx, false) }()
	time.Sleep(50 * time.Millisecond)
	close(g.f.dialBlock)
	<-passed
	if err := <-disconnected; err != nil {
		t.Fatal(err)
	}
	last := g.f.sessions[len(g.f.sessions)-1]
	g.f.mu.Lock()
	closed := last.closed
	g.f.mu.Unlock()
	if !closed || g.s.sess != nil || g.s.Status().State != "disconnected" {
		t.Fatalf("closed %v, %+v", closed, g.s.Status())
	}
}

const older = "old.account@gmail.com"

// withOlder adds a second, earlier account the box still keeps emails of.
func (g *srig) withOlder(t *testing.T) {
	t.Helper()
	if err := g.a.PutAccount(ctx, Account{Address: older, State: "disconnected"}); err != nil {
		t.Fatal(err)
	}
	if err := g.a.Save(ctx, older, 1, t0, rawMail("x@example.com", "kept@x", "", "Kept", "kept")); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveKeepForgetsEveryPassword(t *testing.T) {
	g := newSrig(t)
	g.withOlder(t)
	g.connect(t, "from_now", false)
	g.mail(0, "new")
	g.s.syncOnce(ctx)
	if err := g.s.Remove(ctx, false); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{me, older} {
		acct, err := g.a.Account(ctx, addr)
		if err != nil || acct == nil || acct.State != "disconnected" {
			t.Fatalf("%s: %+v %v", addr, acct, err)
		}
	}
	if g.count(t) != 1 || g.s.Status() != (Status{State: "disconnected", Address: me}) {
		t.Fatalf("kept emails must stay: %d %+v", g.count(t), g.s.Status())
	}
}

func TestRemoveDeleteWipesEveryAccount(t *testing.T) {
	g := newSrig(t)
	g.withOlder(t)
	g.connect(t, "from_now", false)
	g.mail(0, "new")
	g.s.syncOnce(ctx)
	for range 2 {
		if err := g.s.Remove(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, addr := range []string{me, older} {
		if acct, _ := g.a.Account(ctx, addr); acct != nil {
			t.Fatalf("%s still known", addr)
		}
		if n, _ := g.a.Count(ctx, addr); n != 0 {
			t.Fatalf("%s: %d emails left", addr, n)
		}
	}
	if g.s.Status() != (Status{State: "disconnected"}) {
		t.Fatalf("%+v", g.s.Status())
	}
}

func TestRemoveWhileDisconnectedSucceeds(t *testing.T) {
	g := newSrig(t)
	if err := g.s.Remove(ctx, false); err != nil {
		t.Fatal(err)
	}
	g.connect(t, "from_now", false)
	g.s.Disconnect(ctx, false)
	if err := g.s.Remove(ctx, true); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveClosesALiveSession(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	g.s.syncOnce(ctx)
	if g.s.sess == nil {
		t.Fatal("no live session to close")
	}
	last := g.f.sessions[len(g.f.sessions)-1]
	if err := g.s.Remove(ctx, false); err != nil {
		t.Fatal(err)
	}
	g.f.mu.Lock()
	closed := last.closed
	g.f.mu.Unlock()
	if !closed || g.s.sess != nil {
		t.Fatalf("closed %v", closed)
	}
}

func TestSendUsesTheAccountsPassword(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	m := Outgoing{To: "ana@example.com", Subject: "Hi", Text: "x"}
	id, err := g.s.Send(ctx, m)
	if err != nil || !strings.HasSuffix(id, "@concierge") {
		t.Fatal(id, err)
	}
	m.MessageID = id
	if len(g.sent) != 1 || g.sent[0] != (sentMail{me, g.f.password, m}) {
		t.Fatalf("%+v", g.sent)
	}
}

func TestSendWithoutAccountIsErrState(t *testing.T) {
	g := newSrig(t)
	if _, err := g.s.Send(ctx, Outgoing{To: "a@b.c", Subject: "s", Text: "x"}); !errors.Is(err, ErrState) {
		t.Fatalf("disconnected: %v", err)
	}
	g.connect(t, "from_now", false)
	g.f.mu.Lock()
	g.f.password = "changed"
	g.f.mu.Unlock()
	g.s.Disconnect(ctx, false)
	if _, err := g.s.Send(ctx, Outgoing{To: "a@b.c", Subject: "s", Text: "x"}); !errors.Is(err, ErrState) {
		t.Fatalf("after disconnect: %v", err)
	}
	if len(g.sent) != 0 {
		t.Fatalf("%+v", g.sent)
	}
}

func (g *srig) stored(t *testing.T) (string, bool) {
	t.Helper()
	v, ok, err := g.sec.Get(ctx, secretName)
	if err != nil {
		t.Fatal(err)
	}
	return v, ok
}

// logged captures slog's output while the test runs.
func logged(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestConnectKeepsThePasswordInSecretdNotTheRow(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	if v, ok := g.stored(t); !ok || v != g.f.password {
		t.Fatalf("secretd holds %q, %v", v, ok)
	}
	var n int
	if err := g.a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('accounts') WHERE name = 'app_password'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("app_password columns = %d, %v", n, err)
	}
}

func TestConnectWithSecretdDownChangesNothing(t *testing.T) {
	g := newSrig(t)
	s := g.serviceWith(secretstest.Down(t))
	err := s.Connect(ctx, me, g.f.password, "from_now", false)
	if !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if acct, _ := g.a.Account(ctx, me); acct != nil || s.Status().State != "disconnected" {
		t.Fatalf("%+v %+v", acct, s.Status())
	}
}

func TestDialUsesTheSecret(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	s := g.service() // a restart: no live session, the password comes from secretd
	if err := s.resume(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.syncOnce(ctx); err != nil || s.Status().State != "connected" {
		t.Fatalf("%v %+v", err, s.Status())
	}
	if err := g.sec.Put(ctx, secretName, "not-the-password"); err != nil {
		t.Fatal(err)
	}
	s = g.service()
	s.resume(ctx)
	if err := s.syncOnce(ctx); !errors.Is(err, ErrAuth) || s.Status().State != "auth_failed" {
		t.Fatalf("%v %+v", err, s.Status())
	}
}

func TestAConnectedAccountWithoutItsSecretIsAuthFailed(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	g.sec.Delete(ctx, secretName)
	s := g.service()
	s.resume(ctx)
	if err := s.syncOnce(ctx); !errors.Is(err, ErrAuth) || s.Status().State != "auth_failed" {
		t.Fatalf("%v %+v", err, s.Status())
	}
	if acct, _ := g.a.Account(ctx, me); acct.State != "auth_failed" {
		t.Fatalf("%+v", acct)
	}
}

func TestSecretdDownKeepsTheStateAndLogsIt(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	buf := logged(t)
	s := g.serviceWith(secretstest.Down(t))
	s.resume(ctx)
	if err := s.syncOnce(ctx); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("dial: %v", err)
	}
	if _, err := s.Send(ctx, Outgoing{To: "ana@example.com", Subject: "Hi", Text: "x"}); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("send: %v", err)
	}
	s.mu.Lock()
	s.status.State = "connected" // a reconcile runs once the archive session is in
	s.mu.Unlock()
	if err := s.reconcileOnce(ctx); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("reconcile: %v", err)
	}
	if st := s.Status().State; st != "connected" {
		t.Fatalf("state %q", st)
	}
	if acct, _ := g.a.Account(ctx, me); acct.State != "connected" || len(g.sent) != 0 || g.gm.callCount() != 0 {
		t.Fatalf("%+v %+v", acct, g.sent)
	}
	out := buf.String()
	if strings.Count(out, "level=ERROR") != 3 || !strings.Contains(out, "secrets unavailable") || strings.Contains(out, g.f.password) {
		t.Fatalf("log: %s", out)
	}
}

func TestDisconnectDeletesTheSecret(t *testing.T) {
	for _, deleteEmails := range []bool{false, true} {
		g := newSrig(t)
		g.connect(t, "from_now", false)
		if err := g.s.Disconnect(ctx, deleteEmails); err != nil {
			t.Fatal(err)
		}
		if _, ok := g.stored(t); ok {
			t.Fatalf("delete_emails=%v: secret kept", deleteEmails)
		}
	}
}

func TestDisconnectWithSecretdDownChangesNothing(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	s := g.serviceWith(secretstest.Down(t))
	s.resume(ctx)
	if err := s.Disconnect(ctx, false); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if acct, _ := g.a.Account(ctx, me); acct.State != "connected" || s.Status().State != "connecting" {
		t.Fatalf("%+v %+v", acct, s.Status())
	}
}

func TestRemoveDeletesTheSecret(t *testing.T) {
	for _, deleteData := range []bool{false, true} {
		g := newSrig(t)
		g.connect(t, "from_now", false)
		if err := g.s.Remove(ctx, deleteData); err != nil {
			t.Fatal(err)
		}
		if _, ok := g.stored(t); ok {
			t.Fatalf("delete_data=%v: secret kept", deleteData)
		}
	}
}

// The app password secretd couldn't delete is never left behind: Remove fails and nothing changes.
func TestRemoveWithSecretdDownChangesNothing(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	s := g.serviceWith(secretstest.Down(t))
	s.resume(ctx)
	for _, deleteData := range []bool{false, true} {
		if err := s.Remove(ctx, deleteData); !errors.Is(err, secrets.ErrUnavailable) {
			t.Fatalf("delete_data=%v: %v", deleteData, err)
		}
		if acct, _ := g.a.Account(ctx, me); acct.State != "connected" || s.Status().State == "disconnected" {
			t.Fatalf("delete_data=%v: %+v %+v", deleteData, acct, s.Status())
		}
	}
}
