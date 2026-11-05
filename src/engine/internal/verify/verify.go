// Package verify is a connector's setup check: the daemon sends the owner "Concierge check: what is
// A + B?" over its own channel, and the check passes once a reply carrying the sum lands in its
// archive. The challenge lives only in the daemon's memory; the engine asks GET <prefix>/check and,
// once it reads passed, tells hostd the connector is added.
// Design: docs/superpowers/specs/2026-10-28-connectors-write-and-verify-design.md (Phase 1).
package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNotReady is a daemon that cannot send yet (not linked, no account); its Send wraps its own
	// error in this one.
	ErrNotReady  = errors.New("not connected")
	ErrBadNumber = errors.New("number must be + and the country code, then 6–20 digits")
)

// Challenge is one check: the sum asked, when, and where it went (the thread or chat its reply
// must land in).
type Challenge struct {
	A, B  int
	At    time.Time
	Where string
}

// New picks A and B in 10–49 (intN is rand.IntN), so the answer never appears in the question.
func New(intN func(int) int, at time.Time) Challenge {
	return Challenge{A: 10 + intN(40), B: 10 + intN(40), At: at.Truncate(time.Second)}
}

func (c Challenge) Text() string { return fmt.Sprintf("Concierge check: what is %d + %d?", c.A, c.B) }

var firstNumber = regexp.MustCompile(`\d+`)

// Answered is whether reply's first number is the sum: a quoted header further down ("at 14:42")
// never counts, and the question itself (its first number is A) never passes.
func (c Challenge) Answered(reply string) bool {
	n, err := strconv.Atoi(firstNumber.FindString(reply))
	return err == nil && n == c.A+c.B
}

// Daemon is what a connector's daemon gives the check. To, when the check goes where the owner
// says, reads the recipient they typed (its error is shown to them); nil means it goes to the owner's
// own account. Send sends text (to that recipient) and returns where its reply will land; Replies
// returns the texts there since then.
type Daemon struct {
	To      func(string) (string, error)
	Send    func(ctx context.Context, to, text string) (where string, err error)
	Replies func(ctx context.Context, where string, since time.Time) ([]string, error)
	IntN    func(int) int
	Now     func() time.Time
}

// watched is how long a check counts as waiting after it was last sent or polled: the setup dialog
// polls every two seconds, so a check nobody polls for longer was left (the dialog closed).
const watched = 30 * time.Second

var dialable = regexp.MustCompile(`^\+\d{6,20}$`)

// Number is s without whitespace when it is a number a check may go to: + and the country code, as
// ModemManager reports the sender of the reply.
func Number(s string) (string, bool) {
	n := strings.Join(strings.Fields(s), "")
	return n, dialable.MatchString(n)
}

// Phone is a Daemon's To for a check sent to a phone number.
func Phone(s string) (string, error) {
	if n, ok := Number(s); ok {
		return n, nil
	}
	return "", ErrBadNumber
}

// Routes serves POST <prefix>/check (send a new challenge, replacing any earlier one) and GET
// <prefix>/check ({"state": none, waiting or passed}). waiting reports a challenge not yet passed
// that someone is watching (sent or polled within the last 30 s); the daemon may favour its reply
// meanwhile. Neither the recipient nor any text is ever logged.
func Routes(mux *http.ServeMux, prefix string, d Daemon) (waiting func() bool) {
	var (
		mu     sync.Mutex
		cur    *Challenge
		passed bool
		seen   time.Time // when the check was last sent or polled
	)
	mux.HandleFunc("POST "+prefix+"/check", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			To *string `json:"to"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		to, err := "", dec.Decode(&in)
		switch {
		case d.To == nil && (err != nil || in.To != nil):
			err = errors.New("body must be {}")
		case d.To != nil && (err != nil || in.To == nil):
			err = errors.New(`body must be {"to": where the check goes}`)
		case d.To != nil:
			to, err = d.To(*in.To)
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		c := New(d.IntN, d.Now())
		where, err := d.Send(r.Context(), to, c.Text())
		switch {
		case errors.Is(err, ErrNotReady):
			writeJSON(w, http.StatusConflict, map[string]string{"error": ErrNotReady.Error()})
			return
		case err != nil:
			slog.Error("verify: sending the check", "prefix", prefix, "err", fmt.Sprintf("%T", err))
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "check not sent"})
			return
		}
		c.Where = where
		mu.Lock()
		cur, passed, seen = &c, false, d.Now()
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET "+prefix+"/check", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		c, done := cur, passed
		seen = d.Now()
		mu.Unlock()
		switch {
		case c == nil:
			writeJSON(w, http.StatusOK, map[string]string{"state": "none"})
			return
		case done:
			writeJSON(w, http.StatusOK, map[string]string{"state": "passed"})
			return
		}
		replies, err := d.Replies(r.Context(), c.Where, c.At)
		if err != nil {
			slog.Warn("verify: reading the replies", "prefix", prefix, "err", fmt.Sprintf("%T", err))
		}
		for _, reply := range replies {
			if c.Answered(reply) {
				mu.Lock()
				if cur == c { // not replaced by a new check meanwhile
					passed = true
				}
				done = passed && cur == c
				mu.Unlock()
				break
			}
		}
		state := "waiting"
		if done {
			state = "passed"
		}
		writeJSON(w, http.StatusOK, map[string]string{"state": state})
	})
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		return cur != nil && !passed && d.Now().Sub(seen) < watched
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
