package relay

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"concierge/engine/internal/secrets"
	"concierge/engine/internal/secrets/secretstest"
)

type fakeTunnel struct {
	mu        sync.Mutex
	attempts  int      // every Start, failed ones included
	starts    []string // the secrets started with, by the Starts that succeeded
	ids       []Identity
	stops     int
	forgets   int
	forgetErr error
	startErr  error
}

func (f *fakeTunnel) Start(id Identity, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.startErr != nil {
		return f.startErr
	}
	f.starts = append(f.starts, secret)
	f.ids = append(f.ids, id)
	return nil
}
func (f *fakeTunnel) setStartErr(err error) { f.mu.Lock(); f.startErr = err; f.mu.Unlock() }
func (f *fakeTunnel) attemptCount() int     { f.mu.Lock(); defer f.mu.Unlock(); return f.attempts }
func (f *fakeTunnel) Stop()                 { f.mu.Lock(); f.stops++; f.mu.Unlock() }
func (f *fakeTunnel) Forget() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forgetErr != nil {
		return f.forgetErr
	}
	f.forgets++
	return nil
}
func (f *fakeTunnel) forgetCount() int       { f.mu.Lock(); defer f.mu.Unlock(); return f.forgets }
func (f *fakeTunnel) setForgetErr(err error) { f.mu.Lock(); f.forgetErr = err; f.mu.Unlock() }
func (f *fakeTunnel) lastStart() (string, Identity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts[0], f.ids[0]
}
func (f *fakeTunnel) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts), f.stops
}

type rig struct {
	svc    *Service
	store  *Store
	tunnel *fakeTunnel
	sec    *secrets.Client
	cloud  *fakeCtl
}

// fakeCtl is a control plane whose status answer the test sets.
type fakeCtl struct {
	mu        sync.Mutex
	statusErr int // status code to answer with
	boxes     []string
	secrets   []string
}

func newRig(t *testing.T, sec *secrets.Client) *rig {
	t.Helper()
	ctl := &fakeCtl{statusErr: 200}
	cloud := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) {
		ctl.mu.Lock()
		defer ctl.mu.Unlock()
		switch r.URL.Path {
		case "/box/v1/claim/start":
			reply(w, 200, `{}`)
		case "/box/v1/claim/finish":
			reply(w, 200, `{"name":"abcdefghij","box_domain":"example.net","relay":"relay.example.net:7000"}`)
		case "/box/v1/status":
			ctl.boxes = append(ctl.boxes, r.Header.Get("X-Box-Id"))
			ctl.secrets = append(ctl.secrets, r.Header.Get("X-Box-Secret"))
			if ctl.statusErr == 200 {
				reply(w, 200, `{"claimed":true}`)
			} else {
				reply(w, ctl.statusErr, `{}`)
			}
		}
	})
	st, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tun := &fakeTunnel{}
	svc := NewService(st, cloud, sec, tun, func() string { return "" }, time.Now)
	return &rig{svc: svc, store: st, tunnel: tun, sec: sec, cloud: ctl}
}

func (r *rig) setStatus(code int) { r.cloud.mu.Lock(); r.cloud.statusErr = code; r.cloud.mu.Unlock() }

func (r *rig) claim(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := r.svc.ClaimStart(ctx, "a@b.c"); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.ClaimFinish(ctx, "123456"); err != nil {
		t.Fatal(err)
	}
}

func TestClaimStartMakesTheSecretAndKeepsTheEmail(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	r := newRig(t, sec)
	ctx := context.Background()
	if err := r.svc.ClaimStart(ctx, "a@b.c"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := sec.Get(ctx, "box-secret")
	if err != nil || !ok || len(v) != 43 { // 32 bytes, base64url, no padding
		t.Fatalf("secret %q %v %v", v, ok, err)
	}
	if s := r.svc.Status(); s.State != Claiming {
		t.Fatalf("state %v", s.State)
	}
	again := NewService(r.store, r.svc.cloud, sec, r.tunnel, func() string { return "" }, time.Now)
	if s := again.Status(); s.State != Claiming {
		t.Fatalf("after restart: %v", s.State)
	}
	id, _ := r.store.Identity(ctx)
	if id.Email != "a@b.c" {
		t.Fatalf("email %q", id.Email)
	}
}

func TestClaimFinishClaimsAndStartsTheTunnel(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	r := newRig(t, sec)
	r.claim(t)
	s := r.svc.Status()
	if s.State != Claimed || s.Name != "abcdefghij" || s.Address != "abcdefghij.example.net" {
		t.Fatalf("%+v", s)
	}
	v, _, _ := sec.Get(context.Background(), "box-secret")
	if n, _ := r.tunnel.counts(); n != 1 {
		t.Fatalf("starts %d", n)
	}
	if got, tid := r.tunnel.lastStart(); got != v || tid.Relay != "relay.example.net:7000" {
		t.Fatalf("started with %q %+v", got, tid)
	}
	id, _ := r.store.Identity(context.Background())
	if id.Email != "a@b.c" {
		t.Fatalf("email kept: %q", id.Email)
	}
}

func TestClaimFinishWithoutStartIsRefused(t *testing.T) {
	r := newRig(t, secretstest.Start(t, "relay"))
	if err := r.svc.ClaimFinish(context.Background(), "1"); err == nil {
		t.Fatal("want error")
	}
}

func TestStatusWaitingComesFromTheProbe(t *testing.T) {
	r := newRig(t, secretstest.Start(t, "relay"))
	r.claim(t)
	r.svc.probe = func() string { return "certificate" }
	if w := r.svc.Status().Waiting; w != "certificate" {
		t.Fatalf("waiting %q", w)
	}
}

func TestRemovedBoxStartsOver(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	r := newRig(t, sec)
	r.claim(t)
	ctx := context.Background()
	oldID, _ := r.store.Identity(ctx)
	oldSecret, _, _ := sec.Get(ctx, "box-secret")
	r.setStatus(401)
	r.svc.checkStatus(ctx)
	r.cloud.mu.Lock()
	if len(r.cloud.boxes) != 1 || r.cloud.boxes[0] != oldID.BoxID || r.cloud.secrets[0] != oldSecret {
		t.Fatalf("status sent %v %v", r.cloud.boxes, r.cloud.secrets)
	}
	r.cloud.mu.Unlock()
	if r.tunnel.forgetCount() != 1 {
		t.Fatalf("forgets %d", r.tunnel.forgetCount())
	}
	id, _ := r.store.Identity(ctx)
	newSecret, ok, _ := sec.Get(ctx, "box-secret")
	if id.State != Unclaimed || id.BoxID == oldID.BoxID || !ok || newSecret == oldSecret {
		t.Fatalf("%+v ok=%v same=%v", id, ok, newSecret == oldSecret)
	}
	if _, stops := r.tunnel.counts(); stops != 1 {
		t.Fatalf("stops %d", stops)
	}
	if s := r.svc.Status(); s.State != Unclaimed || s.Name != "" {
		t.Fatalf("%+v", s)
	}
}

func TestStatusFailuresChangeNothing(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	r := newRig(t, sec)
	r.claim(t)
	r.setStatus(500)
	for i := 0; i < 20; i++ {
		r.svc.checkStatus(context.Background())
	}
	if s := r.svc.Status(); s.State != Claimed {
		t.Fatalf("%+v", s)
	}
	if starts, stops := r.tunnel.counts(); starts != 1 || stops != 0 {
		t.Fatalf("starts %d stops %d", starts, stops)
	}
}

func TestRunStartsTheTunnelWhenClaimedAndChecksOnEachTick(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	r := newRig(t, sec)
	r.claim(t)
	r.tunnel.mu.Lock()
	r.tunnel.stops = 0
	r.tunnel.starts, r.tunnel.ids = nil, nil
	r.tunnel.mu.Unlock()
	// a new service on the same store, as after a restart
	ticks := make(chan time.Time)
	svc := NewService(r.store, r.svc.cloud, sec, r.tunnel, func() string { return "" }, time.Now)
	svc.ticks = ticks
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	r.setStatus(401)
	ticks <- time.Now()
	ticks <- time.Now() // the first tick is fully handled before the second is taken
	cancel()
	<-done
	if starts, stops := r.tunnel.counts(); starts != 1 || stops < 1 {
		t.Fatalf("starts %d stops %d", starts, stops)
	}
	if s := svc.Status(); s.State != Unclaimed {
		t.Fatalf("%+v", s)
	}
}

func TestClaimStartWithSecretdDown(t *testing.T) {
	r := newRig(t, secretstest.Down(t))
	err := r.svc.ClaimStart(context.Background(), "a@b.c")
	if !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if s := r.svc.Status(); s.State != Unclaimed {
		t.Fatalf("%+v", s)
	}
}

func TestStartOverFailingLeavesClaimedAndTheNextTickCompletesIt(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	r := newRig(t, sec)
	r.claim(t)
	ctx := context.Background()
	old, _ := r.store.Identity(ctx)
	oldSecret, _, _ := sec.Get(ctx, "box-secret")
	r.setStatus(401)
	r.tunnel.setForgetErr(errors.New("disk"))
	r.svc.checkStatus(ctx)
	if id, _ := r.store.Identity(ctx); id.State != Claimed || id.BoxID != old.BoxID {
		t.Fatalf("%+v", id)
	}
	if v, ok, _ := sec.Get(ctx, "box-secret"); !ok || v != oldSecret {
		t.Fatal("secret changed")
	}
	if r.svc.Status().Problem == "" {
		t.Fatal("want a problem")
	}
	r.tunnel.setForgetErr(nil)
	r.svc.checkStatus(ctx)
	id, _ := r.store.Identity(ctx)
	v, ok, _ := sec.Get(ctx, "box-secret")
	if id.State != Unclaimed || id.BoxID == old.BoxID || !ok || v == oldSecret {
		t.Fatalf("%+v %v", id, ok)
	}
	if r.svc.Status().Problem != "" {
		t.Fatal("problem not cleared")
	}
}

func TestStartOverRetriesAfterTheSecretWasDeletedButResetFailed(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	r := newRig(t, sec)
	r.claim(t)
	ctx := context.Background()
	// as if a start-over deleted the secret and then failed
	r.svc.removed = true
	if err := sec.Delete(ctx, "box-secret"); err != nil {
		t.Fatal(err)
	}
	r.svc.checkStatus(ctx)
	if id, _ := r.store.Identity(ctx); id.State != Unclaimed {
		t.Fatalf("%+v", id)
	}
}

// lostKey checks a claimed box whose secret secretd reported absent started over: a new box_id
// and secret, unclaimed, its certificate forgotten, no status call made with a minted secret, and
// the owner told what to do.
func (r *rig) lostKey(t *testing.T, svc *Service, old Identity) {
	t.Helper()
	ctx := context.Background()
	id, _ := r.store.Identity(ctx)
	v, ok, _ := r.sec.Get(ctx, "box-secret")
	if id.State != Unclaimed || id.BoxID == old.BoxID || !ok || v == "" {
		t.Fatalf("after a lost key: %+v secret %v", id, ok)
	}
	if r.tunnel.forgetCount() != 1 {
		t.Fatalf("forgets %d", r.tunnel.forgetCount())
	}
	if p := svc.Status().Problem; p != lostKeyProblem {
		t.Fatalf("problem %q", p)
	}
	if lostKeyProblem != "This box lost its key: claim it again, and remove its old entry in your account" {
		t.Fatalf("the words changed: %q", lostKeyProblem)
	}
	r.cloud.mu.Lock()
	defer r.cloud.mu.Unlock()
	if len(r.cloud.boxes) != 0 {
		t.Fatal("status call made")
	}
}

func TestAClaimedBoxThatLostItsKeyStartsOverOnATick(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	r := newRig(t, sec)
	r.claim(t)
	ctx := context.Background()
	old, _ := r.store.Identity(ctx)
	if err := sec.Delete(ctx, "box-secret"); err != nil {
		t.Fatal(err)
	}
	r.svc.checkStatus(ctx)
	r.lostKey(t, r.svc, old)
	// The words stay until the box is claimed again.
	r.svc.checkStatus(ctx)
	if p := r.svc.Status().Problem; p != lostKeyProblem {
		t.Fatalf("problem %q", p)
	}
	r.claim(t)
	if st := r.svc.Status(); st.State != Claimed || st.Problem != "" {
		t.Fatalf("claimed again: %+v", st)
	}
}

func TestAClaimedBoxThatLostItsKeyStartsOverAtStart(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	r := newRig(t, sec)
	r.claim(t)
	ctx := context.Background()
	old, _ := r.store.Identity(ctx)
	if err := sec.Delete(ctx, "box-secret"); err != nil {
		t.Fatal(err)
	}
	starts, _ := r.tunnel.counts()
	ticks := make(chan time.Time)
	svc := NewService(r.store, r.svc.cloud, sec, r.tunnel, func() string { return "" }, time.Now)
	svc.ticks = ticks
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { svc.Run(runCtx); close(done) }()
	ticks <- time.Now() // the start is fully handled before the first tick is taken
	cancel()
	<-done
	r.lostKey(t, svc, old)
	if now, _ := r.tunnel.counts(); now != starts {
		t.Fatal("the tunnel started without the box's secret")
	}
}

func TestSecretdDownWhileClaimedChangesNothing(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	r := newRig(t, sec)
	r.claim(t)
	ctx := context.Background()
	old, _ := r.store.Identity(ctx)
	starts, stops := r.tunnel.counts()
	check := func(svc *Service) {
		t.Helper()
		if id, _ := r.store.Identity(ctx); id != old {
			t.Fatalf("identity changed: %+v", id)
		}
		if st := svc.Status(); st.State != Claimed || st.Problem != "secrets unavailable" {
			t.Fatalf("%+v", st)
		}
		if a, o := r.tunnel.counts(); a != starts || o != stops || r.tunnel.forgetCount() != 0 {
			t.Fatalf("tunnel touched: starts %d stops %d forgets %d", a, o, r.tunnel.forgetCount())
		}
		if v, ok, _ := sec.Get(ctx, "box-secret"); !ok || v == "" {
			t.Fatal("the secret went")
		}
	}
	// on a tick
	r.svc.sec = secretstest.Down(t)
	r.svc.checkStatus(ctx)
	check(r.svc)
	// at start: no tunnel without the secret, nothing else changed
	ticks := make(chan time.Time)
	svc := NewService(r.store, r.svc.cloud, secretstest.Down(t), r.tunnel, func() string { return "" }, time.Now)
	svc.ticks = ticks
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { svc.Run(runCtx); close(done) }()
	ticks <- time.Now()
	cancel()
	<-done
	stops++ // Run stops the tunnel as it ends
	check(svc)
}

func TestATunnelThatFailedToStartIsRetriedOnTheNextTick(t *testing.T) {
	r := newRig(t, secretstest.Start(t, "relay"))
	ctx := context.Background()
	r.tunnel.setStartErr(errors.New("no CA file"))
	r.claim(t)
	if p := r.svc.Status().Problem; p != "can't start the connection to the relay" {
		t.Fatalf("after the claim: problem %q", p)
	}
	// A good status call doesn't hide it, and the start is tried again.
	r.svc.checkStatus(ctx)
	if p := r.svc.Status().Problem; p != "can't start the connection to the relay" || r.tunnel.attemptCount() != 2 {
		t.Fatalf("problem %q attempts %d", p, r.tunnel.attemptCount())
	}
	// It shows over a status call's problem too.
	r.setStatus(500)
	r.svc.checkStatus(ctx)
	if p := r.svc.Status().Problem; p != "can't start the connection to the relay" || r.tunnel.attemptCount() != 3 {
		t.Fatalf("problem %q attempts %d", p, r.tunnel.attemptCount())
	}
	r.setStatus(200)
	r.tunnel.setStartErr(nil)
	r.svc.checkStatus(ctx)
	if starts, _ := r.tunnel.counts(); starts != 1 || r.tunnel.attemptCount() != 4 {
		t.Fatalf("starts %d attempts %d", starts, r.tunnel.attemptCount())
	}
	if p := r.svc.Status().Problem; p != "" {
		t.Fatalf("problem %q after the tunnel started", p)
	}
	// A running tunnel is left alone.
	r.svc.checkStatus(ctx)
	if r.tunnel.attemptCount() != 4 {
		t.Fatalf("attempts %d: a running tunnel was started again", r.tunnel.attemptCount())
	}
}

func TestConcurrentClaimFinishStartsTheTunnelOnce(t *testing.T) {
	r := newRig(t, secretstest.Start(t, "relay"))
	ctx := context.Background()
	if err := r.svc.ClaimStart(ctx, "a@b.c"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- r.svc.ClaimFinish(ctx, "123456") }()
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
		}
	}
	if starts, _ := r.tunnel.counts(); ok != 1 || starts != 1 {
		t.Fatalf("successes %d starts %d", ok, starts)
	}
}

// A start-over that got past Reset but failed to mint the new secret leaves removed set; a re-claim
// must not be wiped by it.
func TestAReclaimAfterAnUnfinishedStartOverIsNotWiped(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	r := newRig(t, sec)
	ctx := context.Background()
	r.svc.removed = true // the state after Reset succeeded and minting failed: unclaimed, secret absent
	if err := r.svc.ClaimStart(ctx, "a@b.c"); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.ClaimFinish(ctx, "123456"); err != nil {
		t.Fatal(err)
	}
	r.svc.checkStatus(ctx)
	r.cloud.mu.Lock()
	calls := len(r.cloud.boxes)
	r.cloud.mu.Unlock()
	if id, _ := r.store.Identity(ctx); id.State != Claimed || calls != 1 {
		t.Fatalf("%+v status calls %d", id, calls)
	}
	if _, stops := r.tunnel.counts(); stops != 0 || r.tunnel.forgetCount() != 0 {
		t.Fatal("the new claim was torn down")
	}
}

// A device that expires is gone: the list holds only those still let in.
func TestDevicesLeaveOutTheExpired(t *testing.T) {
	r := newRig(t, secretstest.Start(t, "relay"))
	r.claim(t)
	ctx := context.Background()
	now := time.Now()
	if err := r.svc.AddDevice(ctx, "old", "Old phone", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.AddDevice(ctx, "new", "Phone", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ds, err := r.svc.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || ds[0].Serial != "new" {
		t.Fatalf("devices %+v, want only the unexpired one", ds)
	}
}
