package signins

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"concierge/engine/internal/browser"
	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/listen/listentest"
	"concierge/engine/internal/secrets"
	"concierge/engine/internal/secrets/secretstest"
)

// fakeWorker is the browser worker: onAgent plays a run_agent (asking through ask as the real
// worker's value_request would; nil ends it done at once) and onCheck a sign-in check's (a
// registration is followed by one, which a registration's test doesn't script), onCard answers site_card, onRead answers
// read_signed_in (nil: signed in, by the account's email). It notes every request's type in order,
// and the forget_profile / forget_recordings it was sent; down is no worker connected.
type fakeWorker struct {
	mu sync.Mutex
	// down answers every request browser.ErrUnavailable, as the engine's Worker does with none connected.
	down bool
	// order is every request's type, in the order they came.
	order []string
	// forgets is each forget_profile and forget_recordings, its type under "type"; onForget runs as one
	// arrives (nil: answered at once).
	forgets  []browser.Message
	onForget func(typ string, body browser.Message)
	// progress is what each run_agent reports before it plays: step_progress events and payloads.
	progress  []browser.Message
	onAgent   func(ctx context.Context, body browser.Message, ask browser.Ask) (browser.Message, error)
	onCheck   func(ctx context.Context, body browser.Message, ask browser.Ask) (browser.Message, error)
	agentRuns []browser.Message
	onCard    func(body browser.Message) (browser.Message, error)
	cards     []browser.Message
	onRead    func(body browser.Message) (browser.Message, error)
	reads     []browser.Message
	// onArranged answers read_arranged (nil: what arranged() reads); arrangedAsks are its requests.
	onArranged   func(body browser.Message) (browser.Message, error)
	arrangedAsks []browser.Message
}

// tell plays step_progress events to a request's progress.
func tell(events []browser.Message, progress browser.Progress) {
	for _, e := range events {
		if progress != nil {
			payload, _ := e["payload"].(map[string]any)
			progress(e["event"].(string), payload)
		}
	}
}

func (f *fakeWorker) Request(ctx context.Context, typ string, body browser.Message) (browser.Message, error) {
	return f.RequestAsk(ctx, typ, body, nil, nil)
}

func (f *fakeWorker) RequestAsk(ctx context.Context, typ string, body browser.Message, ask browser.Ask, progress browser.Progress) (browser.Message, error) {
	f.mu.Lock()
	if f.down {
		f.mu.Unlock()
		return nil, browser.ErrUnavailable
	}
	f.order = append(f.order, typ)
	f.mu.Unlock()
	switch typ {
	case "forget_profile", "forget_recordings":
		f.mu.Lock()
		forget := browser.Message{"type": typ}
		maps.Copy(forget, body)
		f.forgets = append(f.forgets, forget)
		on := f.onForget
		f.mu.Unlock()
		if on != nil {
			on(typ, body)
		}
		return browser.Message{}, nil
	case "run_agent":
		f.mu.Lock()
		f.agentRuns = append(f.agentRuns, body)
		play, events := f.onAgent, f.progress
		if body["action"] == checkAction {
			play = f.onCheck
		}
		f.mu.Unlock()
		tell(events, progress)
		if play == nil {
			return browser.Message{"outcome": "done", "success": true, "report": "done"}, nil
		}
		return play(ctx, body, ask)
	case "read_signed_in":
		f.mu.Lock()
		f.reads = append(f.reads, body)
		answer := f.onRead
		f.mu.Unlock()
		if answer == nil {
			return browser.Message{"result": "signed_in", "found": "email", "url": body["url"]}, nil
		}
		return answer(body)
	case "read_arranged":
		f.mu.Lock()
		f.arrangedAsks = append(f.arrangedAsks, body)
		answer := f.onArranged
		f.mu.Unlock()
		if answer == nil {
			return browser.Message{"appointment": arranged("Haircut", "2026-11-04", "10:00")}, nil
		}
		return answer(body)
	case "site_card":
		f.mu.Lock()
		f.cards = append(f.cards, body)
		answer := f.onCard
		f.mu.Unlock()
		if answer == nil {
			return browser.Message{"name": "", "description": "", "icon": ""}, nil
		}
		return answer(body)
	}
	return nil, fmt.Errorf("unexpected request %q", typ)
}

// read is the read_signed_in requests so far.
func (f *fakeWorker) read() []browser.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]browser.Message(nil), f.reads...)
}

// forgot is the forget_profile / forget_recordings requests so far.
func (f *fakeWorker) forgot() []browser.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]browser.Message(nil), f.forgets...)
}

// types is every request's type so far, in order.
func (f *fakeWorker) types() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

func (f *fakeWorker) sent() []browser.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]browser.Message(nil), f.agentRuns...)
}

// fakeMail is emaild's status: the mailbox's address, "" when none is connected.
type fakeMail struct{ address string }

func (m fakeMail) Address(context.Context) (string, error) { return m.address, nil }

// emailTo is an incoming email as emaild hands it to a listener.
func emailTo(to, from, subject, html string) *listenv1.Message {
	return &listenv1.Message{Body: &listenv1.Message_Email{Email: &listenv1.Email{
		FromAddr: from, To: []string{"Alex <" + to + ">"}, Subject: subject, Html: html}}}
}

// fakeHost is hostd: the uplink, and the Account's names and phone (err: hostd doesn't answer).
type fakeHost struct {
	link, phone string
	first, last string
	err         error
}

func (h *fakeHost) Uplink(context.Context) (string, error) { return h.link, nil }
func (h *fakeHost) Owner(context.Context) (Owner, error) {
	return Owner{FirstName: h.first, LastName: h.last, Phone: h.phone}, h.err
}

var testNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// testPassword is a registered account's password in the fixture.
const testPassword = "Zk3vQ9pL2mXw7RtY!"

// shopKey is the fixture's site: its card is in the catalog, and its link's host a subdomain.
const shopKey = "shop.test"

type fixture struct {
	svc       *Service
	store     *Store
	storePath string
	worker    *fakeWorker
	sms       *listentest.Fake // smsd's listen service
	inbox     *listentest.Fake // emaild's listen service
	inboxAt   *listentest.Server
	host      *fakeHost
	mail      fakeMail
	// secrets is the engine's secretd, the OpenRouter key in it.
	secrets *secrets.Client
}

// newFixture is a service with shop.test's card in the catalog and no account, with screens and
// recordings on.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, path := openTestStore(t)
	if _, err := store.InsertCard(context.Background(), Card{Key: shopKey, Name: "Shop", Description: "A shop.",
		Link: "https://www.shop.test/start", AddedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: store, storePath: path, worker: &fakeWorker{},
		host:    &fakeHost{link: "wifi", phone: "+351912345678", first: "Alex", last: "Sample"},
		mail:    fakeMail{address: "owner@example.com"},
		secrets: secretstest.Start(t, "engine")}
	f.setKey(t, true)
	var sms listen.Listener
	f.sms, sms = serveSMS(t)
	f.inbox = listentest.NewFake(listen.MatchEmail)
	f.inboxAt = listentest.Serve(t, f.inbox)
	f.svc = NewService(Config{Store: store, Worker: f.worker, Secrets: f.secrets, SMS: sms, Inbox: listen.NewClient(f.inboxAt.Socket),
		Mail: f.mail, Uplink: f.host, Owner: f.host, Now: func() time.Time { return testNow },
		ScreensDir: t.TempDir(), RecordingsDir: t.TempDir(),
		HasKey: func(ctx context.Context) (bool, error) {
			_, ok, err := f.svc.cfg.Secrets.Get(ctx, "openrouter")
			return ok, err
		}})
	f.svc.codes = 200 * time.Millisecond
	return f
}

// setKey keeps the OpenRouter key in secretd, or removes it: the gate refuses every run without it.
func (f *fixture) setKey(t *testing.T, kept bool) {
	t.Helper()
	put := func() error { return f.secrets.Put(context.Background(), "openrouter", "sk-or-v1-test") }
	if !kept {
		put = func() error { return f.secrets.Delete(context.Background(), "openrouter") }
	}
	if err := put(); err != nil {
		t.Fatal(err)
	}
}

// secretdDown stops the service's secretd: nobody answers on its socket.
func (f *fixture) secretdDown(t *testing.T) {
	t.Helper()
	f.svc.cfg.Secrets = secretstest.Down(t)
}

// password is the account's password in secretd; "" with false when there is none.
func (f *fixture) password(t *testing.T) (string, bool) {
	t.Helper()
	pw, ok, err := f.secrets.Get(context.Background(), "signins/"+shopKey+"/password")
	if err != nil {
		t.Fatal(err)
	}
	return pw, ok
}

// signedUp stores the box's account on shop.test in state, testPassword in secretd.
func (f *fixture) signedUp(t *testing.T, state string) {
	t.Helper()
	if err := f.secrets.Put(context.Background(), "signins/"+shopKey+"/password", testPassword); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutAccount(context.Background(), Account{Key: shopKey, Email: "owner+shop@example.com",
		Owner: map[string]string{}, State: state, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) account(t *testing.T) (Account, bool) {
	t.Helper()
	a, ok, err := f.store.Account(context.Background(), shopKey)
	if err != nil {
		t.Fatal(err)
	}
	return a, ok
}

// runText is the newest run's log on shop.test, one line each.
func (f *fixture) runText(t *testing.T) string {
	t.Helper()
	runs, err := f.store.Runs(context.Background(), shopKey)
	if err != nil || len(runs) == 0 {
		t.Fatalf("runs = %v, %v", runs, err)
	}
	return f.runTextOf(t, runs[0].ID)
}

// runTextOf is one run's log on shop.test, one line each.
func (f *fixture) runTextOf(t *testing.T, id int64) string {
	t.Helper()
	_, lines, _, err := f.store.Run(context.Background(), shopKey, id)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%s: %s\n", l.Kind, l.Text)
	}
	return b.String()
}

// logBuffer is the engine's log as a test reads it, while the run's goroutines may still write it.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLog points slog at a logBuffer until the test ends.
func captureLog(t *testing.T, opts *slog.HandlerOptions) *logBuffer {
	t.Helper()
	b := new(logBuffer)
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(b, opts)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return b
}

// tinyPNG is a 1×1 PNG as a card's icon.
const tinyPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
