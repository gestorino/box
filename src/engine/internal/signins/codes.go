package signins

import (
	"context"
	"log/slog"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/listen"
)

// codeTimeout is how long a run waits for an SMS code or for the site's email.
const codeTimeout = 5 * time.Minute

// caughtUpWait bounds how long opening a code wait waits for smsd to say it is listening. Past it
// the flow starts anyway: the client re-opens from the moment it first opened, so a message stored
// meanwhile still arrives.
const caughtUpWait = 10 * time.Second

// codeWait is one run's SMS, from the box's own SIM: a live-only listener opened before the run,
// holding every message that arrives until the run takes one. The code is read by a pattern, never
// by the model: the page's text cannot talk it into a different number.
type codeWait struct {
	mu        sync.Mutex
	msgs      []*listenv1.SMS // oldest first
	arrived   chan struct{}   // a message arrived since Take last looked
	listening atomic.Bool     // smsd said it is listening (its caught_up came)
}

// openCodes opens one listener for every SMS stored from now on, from any sender, and keeps it until
// ctx ends, when smsd forgets it. It returns once smsd has subscribed it, so nothing the site sends
// after this returns is missed, or after wait.
func openCodes(ctx context.Context, sms listen.Listener, wait time.Duration) *codeWait {
	w := &codeWait{arrived: make(chan struct{}, 1)}
	events := sms.Listen(ctx, &listenv1.ListenRequest{})
	ready := make(chan struct{})
	go func() {
		var once sync.Once
		defer once.Do(func() { close(ready) })
		for e := range events {
			if e.GetCaughtUp() != nil {
				w.listening.Store(true)
				once.Do(func() { close(ready) })
				continue
			}
			if m := e.GetMessage().GetSms(); m != nil {
				w.mu.Lock()
				w.msgs = append(w.msgs, m)
				w.mu.Unlock()
				select {
				case w.arrived <- struct{}{}:
				default:
				}
			}
		}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
	case <-time.After(wait):
		slog.Warn("signins: smsd has not answered the code listener yet, starting the flow anyway")
	}
	return w
}

// Listening says smsd has subscribed the listener: until it has, no code can be counted on.
func (w *codeWait) Listening() bool { return w.listening.Load() }

// code is a run of 4 to 8 digits standing alone. Go's regexp has no lookaround: (^|\D) and (\D|$)
// stand for (?<!\d) and (?!\d).
var code = regexp.MustCompile(`(^|\D)(\d{4,8})(\D|$)`)

// Take returns the code of the newest message holding one, waiting for the next arrival that does
// until timeout or ctx ends (ok false). A take discards every message that arrived so far: a later
// ask must not take one of them again. It serves one caller at a time: the worker asks for its values
// one after another.
func (w *codeWait) Take(ctx context.Context, timeout time.Duration) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		w.mu.Lock()
		found, ok := pick(w.msgs)
		if ok {
			w.msgs = nil
		}
		w.mu.Unlock()
		if ok {
			return found, true
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-w.arrived:
		}
	}
}

// pick takes the code of the newest message holding one; msgs are oldest first.
func pick(msgs []*listenv1.SMS) (string, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if match := code.FindStringSubmatch(msgs[i].GetText()); match != nil {
			return match[2], true
		}
	}
	return "", false
}
