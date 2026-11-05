package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"
)

var creds = Creds{ID: 12345, Hash: "0123456789abcdef0123456789abcdef"}

var ana = Self{ID: 1000, Phone: "+351910000000", Name: "Box Concierge"}

func newService(t *testing.T, f *fake, s *Store) *Service {
	t.Helper()
	svc := NewService(f, s, NewSessionStore(s), NewUpdateState(s))
	svc.pairingTimeout = 2 * time.Second
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

// eventually polls cond for up to a second.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

func state(svc *Service) func() string { return func() string { return svc.Status().State } }

func waitState(t *testing.T, svc *Service, want string) {
	t.Helper()
	eventually(t, "state "+want, func() bool { return svc.Status().State == want })
}

func running(f *fake) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

// linked brings a service to connected the way an owner does: credentials, QR, scanned.
func linked(t *testing.T, f *fake, s *Store) *Service {
	t.Helper()
	f.with(func(f *fake) { f.qrSelf = ana })
	svc := newService(t, f, s)
	if err := svc.SetCredentials(ctx, creds); err != nil {
		t.Fatal(err)
	}
	eventually(t, "running", func() bool { return running(f) })
	if err := svc.Link(ctx); err != nil {
		t.Fatal(err)
	}
	waitState(t, svc, "connecting")
	f.emit(Connected{})
	waitState(t, svc, "connected")
	return svc
}

func TestNoCredentialsUntilSet(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := newService(t, f, s)
	if st := svc.Status(); st.State != "no_credentials" || st.Credentials {
		t.Fatalf("status %+v", st)
	}
	if err := svc.Link(ctx); !errors.Is(err, ErrState) {
		t.Fatalf("link without credentials: %v", err)
	}
	if err := svc.SetCredentials(ctx, creds); err != nil {
		t.Fatal(err)
	}
	if st := svc.Status(); st.State != "unlinked" || !st.Credentials || st.APIID != 12345 {
		t.Fatalf("status %+v", st)
	}
	eventually(t, "connected with the credentials", func() bool { return running(f) })
}

func TestCredentialsRefused(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	f.checkErr = ErrCredentials
	svc := newService(t, f, s)
	if err := svc.SetCredentials(ctx, creds); !errors.Is(err, ErrCredentials) {
		t.Fatalf("err %v", err)
	}
	if v, _ := s.Get(ctx, "api_hash"); v != "" {
		t.Fatal("refused hash stored")
	}
	if svc.Status().State != "no_credentials" {
		t.Fatal("state moved")
	}
}

func TestCredentialsOnlyWhileUnlinked(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	if err := svc.SetCredentials(ctx, Creds{ID: 9, Hash: "ffff"}); !errors.Is(err, ErrState) {
		t.Fatalf("err %v", err)
	}
	if v, _ := s.Get(ctx, "api_id"); v != "12345" {
		t.Fatalf("api_id changed to %q", v)
	}
}

func TestQRRotates(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	f.qrURLs = []string{"tg://login?token=AAA", "tg://login?token=BBB"}
	f.qrBlock = make(chan struct{})
	svc := newService(t, f, s)
	svc.SetCredentials(ctx, creds)
	if _, _, ok := svc.QR(); ok {
		t.Fatal("QR before pairing")
	}
	svc.Link(ctx)
	eventually(t, "newest QR", func() bool { url, _, ok := svc.QR(); return ok && url == "tg://login?token=BBB" })
	if _, exp, _ := svc.QR(); exp <= 0 || exp > 30*time.Second {
		t.Fatalf("expires in %v", exp)
	}
	if err := svc.Link(ctx); !errors.Is(err, ErrState) {
		t.Fatalf("second link: %v", err)
	}
}

func TestPairingTimesOut(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	f.qrURLs = []string{"tg://login?token=AAA"}
	f.qrBlock = make(chan struct{}) // never scanned
	svc := newService(t, f, s)
	svc.pairingTimeout = 50 * time.Millisecond
	svc.SetCredentials(ctx, creds)
	svc.Link(ctx)
	waitState(t, svc, "unlinked")
	if _, _, ok := svc.QR(); ok {
		t.Fatal("QR after timeout")
	}
}

func TestLinkSuccessPersistsSession(t *testing.T) {
	s, path := openTemp(t)
	f := newFake()
	ss := NewSessionStore(s)
	ss.StoreSession(ctx, []byte("key"))
	svc := NewService(f, s, ss, NewUpdateState(s))
	svc.Start(ctx)
	f.with(func(f *fake) { f.qrSelf = ana })
	svc.SetCredentials(ctx, creds)
	svc.Link(ctx)
	waitState(t, svc, "connecting")
	if st := svc.Status(); st.Phone != "+351910000000" || st.Name != "Box Concierge" {
		t.Fatalf("status %+v", st)
	}
	svc.Stop()
	s.Close()
	s, _ = reopen(t, path)
	if b, err := NewSessionStore(s).LoadSession(ctx); err != nil || string(b) != "key" {
		t.Fatalf("session %q %v", b, err)
	}
}

func TestPasswordNeededThenWrongThenRight(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	f.qrResult, f.password = ErrPasswordNeeded, "s3cret"
	f.qrSelf = ana
	svc := newService(t, f, s)
	svc.SetCredentials(ctx, creds)
	if err := svc.Password(ctx, "x"); !errors.Is(err, ErrState) {
		t.Fatalf("password while unlinked: %v", err)
	}
	svc.Link(ctx)
	waitState(t, svc, "password_needed")
	if err := svc.Password(ctx, "wrong"); !errors.Is(err, ErrPassword) {
		t.Fatalf("wrong: %v", err)
	}
	if svc.Status().State != "password_needed" {
		t.Fatal("state moved on a wrong password")
	}
	if err := svc.Password(ctx, "s3cret"); err != nil {
		t.Fatal(err)
	}
	waitState(t, svc, "connecting")
}

func TestRestartMidPairingIsUnlinked(t *testing.T) {
	s, path := openTemp(t)
	f := newFake()
	f.qrResult, f.password = ErrPasswordNeeded, "s3cret"
	svc := newService(t, f, s)
	svc.SetCredentials(ctx, creds)
	svc.Link(ctx)
	waitState(t, svc, "password_needed")
	svc.Stop()
	s.Close()
	s, _ = reopen(t, path)
	svc = newService(t, newFake(), s)
	if svc.Status().State != "unlinked" {
		t.Fatalf("state %q", svc.Status().State)
	}
}

func TestStartReconnectsALinkedSession(t *testing.T) {
	s, path := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	svc.Stop()
	s.Close()
	s, _ = reopen(t, path)
	f2 := newFake()
	svc = newService(t, f2, s)
	if st := svc.Status(); st.State != "connecting" || st.Phone != "+351910000000" {
		t.Fatalf("status %+v", st)
	}
	eventually(t, "running", func() bool { return running(f2) })
	f2.emit(Connected{})
	waitState(t, svc, "connected")
}

func TestUnauthorizedPersistsLoggedOut(t *testing.T) {
	s, path := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	f.unauthorized()
	waitState(t, svc, "logged_out")
	if b, _ := s.Get(ctx, "session"); b != "" {
		t.Fatal("session kept after a 401")
	}
	eventually(t, "running again, unauthenticated", func() bool { return running(f) })
	svc.Stop()
	s.Close()
	s, _ = reopen(t, path)
	svc = newService(t, newFake(), s)
	if svc.Status().State != "logged_out" {
		t.Fatalf("state %q after restart", svc.Status().State)
	}
	if err := svc.Link(ctx); err != nil {
		t.Fatalf("link from logged_out: %v", err)
	}
}

func TestUnlinkKeepsOrDeletesChats(t *testing.T) {
	for _, del := range []bool{false, true} {
		s, _ := openTemp(t)
		f := newFake()
		svc := linked(t, f, s)
		s.Save(ctx, row("user:1", 1, "hi", t0))
		if err := svc.Unlink(ctx, del); err != nil {
			t.Fatal(err)
		}
		if svc.Status().State != "unlinked" || !f.loggedOut {
			t.Fatalf("status %+v, logged out %v", svc.Status(), f.loggedOut)
		}
		msgs, _ := s.Messages(ctx, "1000", "user:1", 500)
		if (len(msgs) == 0) != del {
			t.Fatalf("delete=%v: %d messages", del, len(msgs))
		}
		if v, _ := s.Get(ctx, "api_id"); v != "12345" {
			t.Fatal("credentials dropped")
		}
		time.Sleep(50 * time.Millisecond) // a 401 from the logged-out Run must not mark logged_out
		if svc.Status().State != "unlinked" {
			t.Fatalf("state %q", svc.Status().State)
		}
		if err := svc.Unlink(ctx, del); !errors.Is(err, ErrState) {
			t.Fatalf("second unlink: %v", err)
		}
	}
}

func TestConnectedGoesOffline(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	linked(t, f, s)
	eventually(t, "offline", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.offline == 1 })
}

func TestStatusNeverHasHash(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	b, _ := json.Marshal(svc.Status())
	if strings.Contains(string(b), creds.Hash) || strings.Contains(string(b), "hash") {
		t.Fatalf("status %s", b)
	}
	if !strings.Contains(string(b), `"api_id":12345`) || !strings.Contains(string(b), `"archive_channels":false`) {
		t.Fatalf("status %s", b)
	}
}

func TestUnlinkWhenTelegramAlreadyEndedTheSession(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	f.with(func(f *fake) { f.logoutErr = tgerr.New(401, "SESSION_REVOKED") })
	if err := svc.Unlink(ctx, false); err != nil {
		t.Fatal(err)
	}
	if svc.Status().State != "unlinked" {
		t.Fatalf("state %q", svc.Status().State)
	}
}

func TestUnlinkGivesUpWithoutAConnection(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	svc.logoutTimeout = 50 * time.Millisecond
	f.with(func(f *fake) { f.logoutHang = true })
	start := time.Now()
	if err := svc.Unlink(ctx, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > time.Second || svc.Status().State != "connected" {
		t.Fatalf("after %v, state %q", time.Since(start), svc.Status().State)
	}
}

func TestProceedOnlyAfterTheLinkIsStored(t *testing.T) {
	for _, withPassword := range []bool{false, true} {
		s, _ := openTemp(t)
		f := newFake()
		f.qrSelf = ana
		if withPassword {
			f.qrResult, f.password = ErrPasswordNeeded, "s3cret"
		}
		stored := make(chan bool, 1)
		f.onProceed = func() {
			v, _ := s.Get(ctx, "self")
			stored <- v != ""
		}
		svc := newService(t, f, s)
		svc.SetCredentials(ctx, creds)
		svc.Link(ctx)
		if withPassword {
			waitState(t, svc, "password_needed")
			if err := svc.Password(ctx, "s3cret"); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case ok := <-stored:
			if !ok {
				t.Fatalf("password=%v: Run proceeded before the link was stored", withPassword)
			}
		case <-time.After(time.Second):
			t.Fatalf("password=%v: never proceeded", withPassword)
		}
	}
}

func TestRunsNeverOverlap(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	svc.SetArchiveChannels(ctx, false)             // restarts the run
	if err := svc.Unlink(ctx, false); err != nil { // and again
		t.Fatal(err)
	}
	eventually(t, "three runs", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.runs) == 3 && f.running })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.overlapped {
		t.Fatal("a run started before the previous one ended")
	}
}

func TestRemoveConnectedKeep(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	s.Save(ctx, row("user:1", 1, "hi", t0))
	if err := svc.Remove(ctx, false, false); err != nil {
		t.Fatal(err)
	}
	if svc.Status().State != "unlinked" || !f.loggedOut {
		t.Fatalf("status %+v, logged out %v", svc.Status(), f.loggedOut)
	}
	if msgs, _ := s.Messages(ctx, "1000", "user:1", 500); len(msgs) != 1 {
		t.Fatal("kept messages lost")
	}
	if v, _ := s.Get(ctx, "api_id"); v != "12345" {
		t.Fatal("keep must keep the credentials")
	}
	if v, _ := s.Get(ctx, "self"); v != "" {
		t.Fatal("the link stayed")
	}
}

func TestRemoveDeleteWipesCredentialsToo(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	s.Save(ctx, row("user:1", 1, "hi", t0))
	other := row("user:2", 2, "older account", t0)
	other.Account = "2000"
	s.Save(ctx, other)
	if err := svc.Remove(ctx, true, false); err != nil {
		t.Fatal(err)
	}
	for _, acct := range []string{"1000", "2000"} {
		if chats, _ := s.Chats(ctx, acct, ""); len(chats) != 0 {
			t.Fatalf("%s: %+v", acct, chats)
		}
	}
	for _, k := range []string{"api_id", "api_hash", "self", "session"} {
		if v, _ := s.Get(ctx, k); v != "" {
			t.Fatalf("%s left", k)
		}
	}
	if svc.Status().State != "no_credentials" {
		t.Fatalf("state %q", svc.Status().State)
	}
}

func TestRemoveOfflineChangesNothing(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	s.Save(ctx, row("user:1", 1, "hi", t0))
	svc.logoutTimeout = 50 * time.Millisecond
	f.with(func(f *fake) { f.logoutHang = true })
	if err := svc.Remove(ctx, true, false); !errors.Is(err, ErrLogout) {
		t.Fatalf("err %v", err)
	}
	if msgs, _ := s.Messages(ctx, "1000", "user:1", 500); len(msgs) != 1 || svc.Status().State != "connected" {
		t.Fatalf("changed: %d messages, state %q", len(msgs), svc.Status().State)
	}
	if v, _ := s.Get(ctx, "self"); v == "" {
		t.Fatal("the link went")
	}
}

func TestRemoveForce(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	svc.logoutTimeout = 50 * time.Millisecond
	f.with(func(f *fake) { f.logoutHang = true })
	if err := svc.Remove(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get(ctx, "self"); v != "" || svc.Status().State != "unlinked" {
		t.Fatalf("self %q, state %q", v, svc.Status().State)
	}
	time.Sleep(50 * time.Millisecond) // a 401 from the forgotten session must not mark logged_out
	if svc.Status().State != "unlinked" {
		t.Fatalf("state %q", svc.Status().State)
	}
}

func TestRemoveMidPairingEndsIt(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	f.qrSelf = ana
	f.qrBlock = make(chan struct{})
	svc := newService(t, f, s)
	svc.SetCredentials(ctx, creds)
	svc.Link(ctx)
	waitState(t, svc, "pairing")
	if err := svc.Remove(ctx, false, false); err != nil {
		t.Fatal(err)
	}
	close(f.qrBlock) // the QR login would now succeed — the cancelled pairing must not link
	time.Sleep(50 * time.Millisecond)
	if v, _ := s.Get(ctx, "self"); v != "" || svc.Status().State != "unlinked" {
		t.Fatalf("self %q, state %q", v, svc.Status().State)
	}
}

func TestRemovePasswordNeeded(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	f.qrSelf, f.qrResult, f.password = ana, ErrPasswordNeeded, "s3cret"
	svc := newService(t, f, s)
	svc.SetCredentials(ctx, creds)
	svc.Link(ctx)
	waitState(t, svc, "password_needed")
	if err := svc.Remove(ctx, true, false); err != nil {
		t.Fatal(err)
	}
	if svc.Status().State != "no_credentials" {
		t.Fatalf("state %q", svc.Status().State)
	}
	if err := svc.Password(ctx, "s3cret"); !errors.Is(err, ErrState) {
		t.Fatalf("password after remove: %v", err)
	}
}

func TestRemoveTwice(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	for range 2 {
		if err := svc.Remove(ctx, false, false); err != nil {
			t.Fatal(err)
		}
	}
	svc2 := newService(t, newFake(), func() *Store { s, _ := openTemp(t); return s }())
	if err := svc2.Remove(ctx, true, false); err != nil { // no credentials at all
		t.Fatal(err)
	}
}

func TestRemoveDeleteWaitsForTheRunToEnd(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	f.with(func(f *fake) {
		f.onRunEnd = func() { s.PutPeer(ctx, Peer{Account: "1000", Key: "user:9", Name: "Late Update"}) }
	})
	if err := svc.Remove(ctx, true, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // a run still winding down would write now
	if p, ok, _ := s.Peer(ctx, "1000", "user:9"); ok {
		t.Fatalf("written after the wipe: %+v", p)
	}
}
