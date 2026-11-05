package telegram

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// fake stands in for gotd. Tests drive it: run blocks until ctx ends or unauthorized() is called;
// qr shows the URLs queued in qrURLs, then returns qrResult.
type fake struct {
	mu sync.Mutex

	checkErr    error
	checked     []Creds
	runs        []Creds
	running     bool
	events      func(any)
	unauth      chan struct{}
	overlapped  bool // a Run started while another was still running
	qrURLs      []string
	qrResult    error
	qrSelf      Self
	qrBlock     chan struct{} // when set, QR waits on it after showing its URLs
	password    string        // the right one
	loggedOut   bool
	logoutErr   error
	logoutHang  bool // LogOut waits for its ctx: no connection
	offline     int
	onProceed   func() // called by Proceed
	proceeded   int
	dialogs     []Dialog
	dialogRuns  int // Dialogs calls
	history     map[string][]tg.MessageClass
	historyEnt  tg.Entities
	asked       []string         // History peers, in order
	historyErr  map[string]error // answered once per peer
	historyHold chan struct{}    // when set, History waits on it
	onRunEnd    func()           // called as a cancelled Run winds down (an update still being stored)
	sent        []string         // Send calls: "key hash text"
	sendErr     error
	read        []string // ReadHistory calls that went through: "ReadHistory key maxID"
	readErr     error
	readStuck   bool // ReadHistory blocks until its context ends
}

func (f *fake) ReadHistory(ctx context.Context, p InputPeer, maxID int) error {
	f.mu.Lock()
	stuck := f.readStuck
	f.mu.Unlock()
	if stuck {
		<-ctx.Done()
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return f.readErr
	}
	f.read = append(f.read, fmt.Sprintf("ReadHistory %s %d", p.Key, maxID))
	return nil
}

func (f *fake) Send(_ context.Context, p InputPeer, text string) (int, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, fmt.Sprintf("%s %d %s", p.Key, p.AccessHash, text))
	if f.sendErr != nil {
		return 0, time.Time{}, f.sendErr
	}
	return 77, t0, nil
}

func newFake() *fake {
	return &fake{unauth: make(chan struct{}, 1), history: map[string][]tg.MessageClass{}}
}

func (f *fake) Check(_ context.Context, c Creds) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checked = append(f.checked, c)
	return f.checkErr
}

func (f *fake) Run(ctx context.Context, c Creds, events func(any)) error {
	f.mu.Lock()
	f.runs = append(f.runs, c)
	if f.running {
		f.overlapped = true
	}
	f.running, f.events = true, events
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.running = false; f.mu.Unlock() }()
	select {
	case <-ctx.Done():
		time.Sleep(20 * time.Millisecond) // gotd takes a moment to close its connections
		f.mu.Lock()
		end := f.onRunEnd
		f.mu.Unlock()
		if end != nil {
			end()
		}
		return ctx.Err()
	case <-f.unauth:
		return tgerr.New(401, "SESSION_REVOKED")
	}
}

// unauthorized makes the running Run return a 401, as when the session is ended on the phone.
func (f *fake) unauthorized() { f.unauth <- struct{}{} }

func (f *fake) emit(e any) {
	f.mu.Lock()
	h := f.events
	f.mu.Unlock()
	h(e)
}

func (f *fake) QR(ctx context.Context, show func(url string, expires time.Time)) (Self, error) {
	f.mu.Lock()
	urls, res, self, block := f.qrURLs, f.qrResult, f.qrSelf, f.qrBlock
	f.mu.Unlock()
	for _, u := range urls {
		show(u, time.Now().Add(30*time.Second))
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return Self{}, ctx.Err()
		}
	}
	return self, res
}

func (f *fake) Password(_ context.Context, pw string) (Self, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pw != f.password {
		return Self{}, ErrPassword
	}
	return f.qrSelf, nil
}

func (f *fake) LogOut(ctx context.Context) error {
	f.mu.Lock()
	hang, err := f.logoutHang, f.logoutErr
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loggedOut = true
	return err
}

func (f *fake) Offline(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offline++
	return nil
}

func (f *fake) Dialogs(_ context.Context, page func(Dialog) error) error {
	f.mu.Lock()
	ds := f.dialogs
	f.dialogRuns++
	f.dialogRuns++
	f.mu.Unlock()
	for _, d := range ds {
		if err := page(d); err != nil {
			return err
		}
	}
	return nil
}

func (f *fake) History(_ context.Context, p InputPeer, limit int) ([]tg.MessageClass, tg.Entities, error) {
	f.mu.Lock()
	hold := f.historyHold
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, p.Key)
	if err := f.historyErr[p.Key]; err != nil {
		delete(f.historyErr, p.Key)
		return nil, tg.Entities{}, err
	}
	msgs := f.history[p.Key]
	if len(msgs) > limit {
		msgs = msgs[:limit]
	}
	return msgs, f.historyEnt, nil
}

func (f *fake) with(fn func(f *fake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fake) Proceed() {
	f.mu.Lock()
	f.proceeded++
	h := f.onProceed
	f.mu.Unlock()
	if h != nil {
		h()
	}
}
