package signins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"concierge/engine/internal/browser"
)

// waitHook stands in for the wait's timer: opened gets each wait's duration once the wait is open
// (its phase and deadline visible), and a value sent on expire ends the wait that is open.
type waitHook struct {
	opened chan time.Duration
	expire chan time.Time
}

func (f *fixture) hookWaits() *waitHook {
	h := &waitHook{opened: make(chan time.Duration, 4), expire: make(chan time.Time)}
	f.svc.cfg.After = func(d time.Duration) <-chan time.Time {
		h.opened <- d
		return h.expire
	}
	return h
}

// clock is a Now the test moves.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// answer is what one ask came back with.
type answer struct {
	value string
	ok    bool
}

// asksCaptcha plays an errand that meets a captcha n times, then ends done, sending each answer on
// got; an ask answered no ends it as the real worker's would (the model can't go on).
func asksCaptcha(n int, got chan<- answer) func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
	return func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		for range n {
			v, ok := ask(ctx, browser.AskRequest{Name: "human_check", Why: "an hCaptcha appeared"})
			got <- answer{v, ok}
			if !ok {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return browser.Message{"outcome": "failed", "success": false, "report": ""}, nil
			}
		}
		return browser.Message{"outcome": "done", "success": true, "report": "done"}, nil
	}
}

func opened(t *testing.T, h *waitHook) {
	t.Helper()
	select {
	case d := <-h.opened:
		if d != 10*time.Minute {
			t.Fatalf("the wait is %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no wait opened")
	}
}

func before(lines []string, first, then string) bool {
	i, j := -1, -1
	for n, l := range lines {
		if l == first && i < 0 {
			i = n
		}
		if l == then {
			j = n
		}
	}
	return i >= 0 && j > i
}

// view is shop.test's card as the Accounts page shows it.
func (f *fixture) view(t *testing.T) SiteView {
	t.Helper()
	sites, err := f.svc.Sites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range sites {
		if v.Key == shopKey {
			return v
		}
	}
	t.Fatal("no shop.test card")
	return SiteView{}
}

func (f *fixture) captchaErrand(t *testing.T, n int, got chan<- answer) int64 {
	t.Helper()
	f.worker.onAgent = asksCaptcha(n, got)
	id, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/a", Action: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestACaptchaWaitsAndDoneCarriesOn(t *testing.T) {
	f := newFixture(t)
	h := f.hookWaits()
	got := make(chan answer, 1)
	id := f.captchaErrand(t, 1, got)
	opened(t, h)

	if v := f.view(t); v.Phase != phaseWaiting || v.Run != id {
		t.Fatalf("view while waiting = %q %d", v.Phase, v.Run)
	}
	if err := f.svc.Done(shopKey); err != nil {
		t.Fatal(err)
	}
	if a := <-got; a != (answer{"done", true}) {
		t.Fatalf("the ask = %+v", a)
	}
	f.svc.runs.Wait()
	_, lines := lineTexts(t, f.store, shopKey, id)
	if !before(lines, "box: Waiting for you to solve a captcha", "box: You solved it") {
		t.Fatalf("lines:\n%s", strings.Join(lines, "\n"))
	}
	if v := f.view(t); v.Phase != "" || v.Run != 0 {
		t.Fatalf("view after = %q %d", v.Phase, v.Run)
	}
	// The wait is over: a second Done has nothing to answer.
	if err := f.svc.Done(shopKey); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("a second Done: %v", err)
	}
}

func TestAnUnansweredCaptchaWaitEndsAfterTenMinutes(t *testing.T) {
	f := newFixture(t)
	h := f.hookWaits()
	got := make(chan answer, 1)
	id := f.captchaErrand(t, 1, got)
	opened(t, h)
	h.expire <- testNow.Add(10 * time.Minute)
	if a := <-got; a.ok {
		t.Fatalf("the ask = %+v", a)
	}
	f.svc.runs.Wait()
	if d, _, _ := f.svc.AgentRun(context.Background(), id); d.State != "ended" || d.Reason != "the captcha wasn't solved" {
		t.Fatalf("errand = %s %q", d.State, d.Reason)
	}
	if _, lines := lineTexts(t, f.store, shopKey, id); !before(lines, "box: Waiting for you to solve a captcha", "box: Nobody came in 10 minutes") {
		t.Fatalf("lines:\n%s", strings.Join(lines, "\n"))
	}
}

// Done means "look again": a captcha still there is asked about again, with a fresh 10 minutes.
func TestASecondCaptchaAskWaitsAgain(t *testing.T) {
	f := newFixture(t)
	c := &clock{now: testNow}
	f.svc.cfg.Now = c.Now
	h := f.hookWaits()
	got := make(chan answer, 2)
	f.captchaErrand(t, 2, got)
	opened(t, h)
	c.set(testNow.Add(4 * time.Minute))
	if err := f.svc.Done(shopKey); err != nil {
		t.Fatal(err)
	}
	opened(t, h)
	if _, until, _ := f.svc.running(shopKey); until == nil || !until.Equal(testNow.Add(14*time.Minute)) {
		t.Fatalf("second wait until %v", until)
	}
	if err := f.svc.Done(shopKey); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	for range 2 {
		if a := <-got; a != (answer{"done", true}) {
			t.Fatalf("an ask = %+v", a)
		}
	}
}

// A worker that dies cancels the ask's context: the wait ends with it, not after 10 minutes.
func TestAWaitEndsWhenTheWorkerGoes(t *testing.T) {
	f := newFixture(t)
	h := f.hookWaits()
	var phase string
	f.worker.onAgent = func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		askCtx, cancel := context.WithCancel(ctx)
		go func() { <-h.opened; cancel() }()
		if _, ok := ask(askCtx, browser.AskRequest{Name: "human_check", Why: "an hCaptcha appeared"}); ok {
			return nil, errors.New("human_check was answered")
		}
		phase = f.svc.phase(shopKey)
		return nil, browser.ErrUnavailable
	}
	id, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/a", Action: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	if phase != phaseErrand {
		t.Fatalf("the phase after the wait = %q", phase)
	}
	if p := f.svc.phase(shopKey); p != "" {
		t.Fatalf("the site is still %s", p)
	}
	if err := f.svc.Done(shopKey); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("Done after the run: %v", err)
	}
	if d, _, _ := f.svc.AgentRun(context.Background(), id); d.Reason != "the browser worker was away" {
		t.Fatalf("errand = %s %q", d.State, d.Reason)
	}
}

func TestDoneWithoutAWaitIsRefused(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.Done(shopKey); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("Done: %v", err)
	}
	if err := f.svc.GiveUp(shopKey); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("GiveUp: %v", err)
	}
}

// openWait claims the site and opens its run's log, for a test that holds a wait itself.
func (f *fixture) openWait(t *testing.T) *runLog {
	t.Helper()
	if !f.svc.claim(shopKey, phaseErrand) {
		t.Fatal("claim")
	}
	log, err := f.svc.startRun(context.Background(), shopKey, "errand")
	if err != nil {
		t.Fatal(err)
	}
	return log
}

// The owner's Done lands at the moment the wait ends (its 10 minutes, or the run going): the
// console was told it worked, so the run carries on as solved. Both are ready at once, so the
// wait's select is a coin toss; enough tosses that a wait that ignores the answer loses one.
func TestDoneAsTheWaitEndsStillCounts(t *testing.T) {
	for _, ending := range []string{"timeout", "ctx"} {
		f := newFixture(t)
		log := f.openWait(t)
		for i := range 32 {
			ctx, cancel := context.WithCancel(context.Background())
			var doneErr error
			f.svc.cfg.After = func(time.Duration) <-chan time.Time {
				doneErr = f.svc.Done(shopKey) // the wait is open: its phase and deadline are set
				if ending == "ctx" {
					cancel()
					return nil
				}
				fired := make(chan time.Time, 1)
				fired <- testNow
				return fired
			}
			solved := f.svc.waitForOwner(ctx, shopKey, log, nil)
			cancel()
			if doneErr != nil || !solved {
				t.Fatalf("%s, toss %d: Done = %v, solved = %v", ending, i, doneErr, solved)
			}
		}
	}
}
