package calls

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"concierge/engine/internal/hostsettings"
)

var ErrBusy = errors.New("a call is already in progress")

// talkLimit caps the check from its answer (a prompt call has promptLimit).
const talkLimit = 60 * time.Second

// Caller places one call at a time: it dials, waits up to ring for an answer, holds a conversation
// (Converse), and hangs up — always, whatever happened in between. The archiver records the call
// like any other.
type Caller struct {
	m          Modem
	a          Audio
	d          Duplex
	hold       func(path string) (release func())
	keep       func(path string, k Kept)
	settings   func() (hostsettings.CallsKeep, bool)
	tmp        string
	now        func() time.Time
	ring, poll time.Duration
	busy       sync.Mutex
}

// NewCaller: hold is the archiver's Hold, so the archiver does not clear the call from ModemManager
// before the Caller has read how it ended; keep is the archiver's Keep, which puts what a held call
// left behind on the call's record. settings says what the owner lets a call keep beyond its
// summary (known is false until hostd first answers); tmp is a directory for a recording in
// progress, on the same filesystem as the archive's audio.
func NewCaller(m Modem, a Audio, d Duplex, hold func(path string) (release func()), keep func(path string, k Kept),
	settings func() (hostsettings.CallsKeep, bool), tmp string, ring, poll time.Duration) *Caller {
	return &Caller{m: m, a: a, d: d, hold: hold, keep: keep, settings: settings, tmp: tmp, now: time.Now, ring: ring, poll: poll}
}

// connect dials number and waits for the answer. Answered, the call's audio is routed and out is "";
// otherwise out says how it ended (busy, no-answer, failed). finish — always to be called, once —
// unroutes what was routed, hangs up and releases the archiver's hold, in that order and whatever
// the context. An error means nothing was dialled.
func (c *Caller) connect(ctx context.Context, number string) (path, out string, finish func(), err error) {
	instance, path, err := c.m.Dial(ctx, number)
	if err != nil {
		return "", "", nil, err
	}
	release := c.hold(path)
	routed := false
	finish = func() {
		bg := context.WithoutCancel(ctx)
		if routed {
			uctx, cancel := context.WithTimeout(bg, 10*time.Second)
			if err := c.a.Unroute(uctx); err != nil {
				slog.Error("calls: unrouting the call's audio", "err", err)
			}
			cancel()
		}
		hctx, cancel := context.WithTimeout(bg, 10*time.Second)
		if err := c.m.Hangup(hctx, instance, path); err != nil {
			slog.Warn("calls: hanging up", "err", fmt.Sprintf("%T", err)) // usually already ended
		}
		cancel()
		release() // last, after the hangup
	}
	if out := c.answered(ctx, path); out != "" {
		return path, out, finish, nil
	}
	routed = true // unroute even when routing failed halfway
	if err := c.a.Route(ctx); err != nil {
		slog.Error("calls: routing the call's audio", "err", err)
		return path, "failed", finish, nil
	}
	return path, "", finish, nil
}

// answered waits for the call to go active (""), or says how it ended before that.
func (c *Caller) answered(ctx context.Context, path string) string {
	deadline := time.Now().Add(c.ring)
	for {
		if state, reason, ok := c.state(ctx, path); ok {
			switch state {
			case "gone":
				return "failed" // ModemManager restarted
			case "active":
				return ""
			case "terminated":
				return outcome("outgoing", false, reason)
			}
		}
		if time.Now().After(deadline) {
			return "no-answer"
		}
		select {
		case <-ctx.Done():
			return "failed"
		case <-time.After(c.poll):
		}
	}
}

// state is the call's state in ModemManager ("gone" when it is no longer listed); ok is false when
// the list could not be read.
func (c *Caller) state(ctx context.Context, path string) (state, reason string, ok bool) {
	_, calls, err := c.m.List(ctx)
	if err != nil {
		return "", "", false
	}
	i := slices.IndexFunc(calls, func(m ModemCall) bool { return m.Path == path })
	if i < 0 {
		return "gone", "", true
	}
	return calls[i].State, calls[i].Reason, true
}
