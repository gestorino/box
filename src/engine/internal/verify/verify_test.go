package verify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 500, time.UTC)

func TestChallengeText(t *testing.T) {
	c := New(func(int) int { return 13 }, t0) // A = B = 23
	if c.Text() != "Concierge check: what is 23 + 23?" {
		t.Fatal(c.Text())
	}
	if !c.At.Equal(t0.Truncate(time.Second)) {
		t.Fatalf("at %v", c.At)
	}
}

func TestChallengeRange(t *testing.T) {
	lo, hi := New(func(int) int { return 0 }, t0), New(func(n int) int { return n - 1 }, t0)
	if lo.A != 10 || lo.B != 10 || hi.A != 49 || hi.B != 49 {
		t.Fatalf("%+v %+v", lo, hi)
	}
}

func TestAnsweredIsTheFirstNumber(t *testing.T) {
	c := Challenge{A: 23, B: 19}
	for reply, want := range map[string]bool{
		"42": true, "It's 42!": true, " 42\n": true, "4 2": false, "41": false, "": false, "420": false,
		"41\n\nOn Wed, 28 Oct 2026 at 14:42 Box <b@x> wrote:\n> Concierge check: what is 23 + 19?": false,
		"42\n\nOn Wed, 28 Oct 2026 at 14:41 wrote:":                                                true,
		"Concierge check: what is 23 + 19?":                                                        false,
	} {
		if got := c.Answered(reply); got != want {
			t.Errorf("%q: %v", reply, got)
		}
	}
}

// rig is a Daemon whose Send records where it went and whose Replies serve replies.
type rig struct {
	mu      sync.Mutex
	sent    []string // to|text
	sendErr error
	replies []string
	repErr  error
	where   string
	since   time.Time
	ints    []int
	later   time.Duration // how far the clock has moved past t0
}

func (g *rig) daemon(to func(string) (string, error)) Daemon {
	return Daemon{
		To: to,
		Send: func(_ context.Context, to, text string) (string, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.sendErr != nil {
				return "", g.sendErr
			}
			g.sent = append(g.sent, to+"|"+text)
			return "chat-1", nil
		},
		Replies: func(_ context.Context, where string, since time.Time) ([]string, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.where, g.since = where, since
			return g.replies, g.repErr
		},
		IntN: func(int) int {
			g.mu.Lock()
			defer g.mu.Unlock()
			n := g.ints[0]
			if len(g.ints) > 1 {
				g.ints = g.ints[1:]
			}
			return n
		},
		Now: func() time.Time {
			g.mu.Lock()
			defer g.mu.Unlock()
			return t0.Add(g.later)
		},
	}
}

func serve(g *rig, to func(string) (string, error)) (http.Handler, func() bool) {
	mux := http.NewServeMux()
	waiting := Routes(mux, "/x", g.daemon(to))
	return mux, waiting
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func state(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := do(h, http.MethodGet, "/x/check", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET check: %d %s", rec.Code, rec.Body)
	}
	return strings.TrimSpace(rec.Body.String())
}

func TestRoutesSendWaitPass(t *testing.T) {
	g := &rig{ints: []int{13, 9}} // 23 + 19
	h, waiting := serve(g, nil)
	if s := state(t, h); s != `{"state":"none"}` || waiting() {
		t.Fatalf("before: %s %v", s, waiting())
	}
	if rec := do(h, http.MethodPost, "/x/check", `{}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(g.sent) != 1 || g.sent[0] != "|Concierge check: what is 23 + 19?" {
		t.Fatalf("%q", g.sent)
	}
	g.replies = []string{"41", "hello"}
	if s := state(t, h); s != `{"state":"waiting"}` || !waiting() {
		t.Fatalf("waiting: %s", s)
	}
	g.replies = []string{"41", "42"}
	if s := state(t, h); s != `{"state":"passed"}` || waiting() {
		t.Fatalf("passed: %s", s)
	}
	g.replies = nil // passed stays passed
	if s := state(t, h); s != `{"state":"passed"}` {
		t.Fatalf("still passed: %s", s)
	}
}

func TestRepliesGetSinceTheChallenge(t *testing.T) {
	g := &rig{ints: []int{0}}
	h, _ := serve(g, nil)
	do(h, http.MethodPost, "/x/check", `{}`)
	state(t, h)
	if g.where != "chat-1" || !g.since.Equal(t0.Truncate(time.Second)) {
		t.Fatalf("%q %v", g.where, g.since)
	}
}

func TestRoutesRecipientRequiredAndValidated(t *testing.T) {
	g := &rig{ints: []int{0}}
	h, _ := serve(g, Phone)
	for _, body := range []string{`{}`, `{"to":"+351911111111","x":1}`, `nope`} {
		rec := do(h, http.MethodPost, "/x/check", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `body must be {\"to\": where the check goes}`) {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	for _, body := range []string{`{"to":"911111111"}`, `{"to":"+35"}`, `{"to":"+351x"}`} {
		rec := do(h, http.MethodPost, "/x/check", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "number must be + and the country code, then 6–20 digits") {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := do(h, http.MethodPost, "/x/check", `{"to":"+351 911 111 111"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(g.sent) != 1 || !strings.HasPrefix(g.sent[0], "+351911111111|") {
		t.Fatalf("%q", g.sent)
	}
	if s := state(t, h); s != `{"state":"waiting"}` {
		t.Fatal(s)
	}
}

func TestRoutesNoRecipientWhenNotNeeded(t *testing.T) {
	g := &rig{ints: []int{0}}
	h, _ := serve(g, nil)
	if rec := do(h, http.MethodPost, "/x/check", `{"to":"+351911111111"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
}

func TestRoutesSendAgainReplacesTheChallenge(t *testing.T) {
	g := &rig{ints: []int{13, 13, 1, 1}} // 23 + 23 = 46, then 11 + 11 = 22
	h, _ := serve(g, nil)
	do(h, http.MethodPost, "/x/check", `{}`)
	do(h, http.MethodPost, "/x/check", `{}`)
	g.replies = []string{"46"}
	if s := state(t, h); s != `{"state":"waiting"}` {
		t.Fatalf("an old answer passed: %s", s)
	}
	g.replies = []string{"22"}
	if s := state(t, h); s != `{"state":"passed"}` {
		t.Fatal(s)
	}
	do(h, http.MethodPost, "/x/check", `{}`) // a new check after a pass waits again
	g.replies = nil
	if s := state(t, h); s != `{"state":"waiting"}` {
		t.Fatal(s)
	}
}

func TestRoutesMapsErrors(t *testing.T) {
	g := &rig{ints: []int{0}, sendErr: fmt.Errorf("whatsapp: %w", ErrNotReady)}
	h, _ := serve(g, nil)
	if rec := do(h, http.MethodPost, "/x/check", `{}`); rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"not connected"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	g.sendErr = errors.New("boom")
	if rec := do(h, http.MethodPost, "/x/check", `{}`); rec.Code != http.StatusBadGateway || strings.TrimSpace(rec.Body.String()) != `{"error":"check not sent"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if s := state(t, h); s != `{"state":"none"}` {
		t.Fatalf("a failed send left %s", s)
	}
}

func TestRoutesNeverLogNumberOrText(t *testing.T) {
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	g := &rig{ints: []int{0}, sendErr: errors.New("send to +351911111111 failed: Concierge check")}
	h, _ := serve(g, Phone)
	do(h, http.MethodPost, "/x/check", `{"to":"+351911111111"}`)
	g.sendErr, g.repErr = nil, errors.New("reading +351911111111: Concierge check")
	do(h, http.MethodPost, "/x/check", `{"to":"+351911111111"}`)
	if s := state(t, h); s != `{"state":"waiting"}` {
		t.Fatal(s)
	}
	if logs.Len() == 0 {
		t.Fatal("the failures were not logged at all")
	}
	if strings.Contains(logs.String(), "911111111") || strings.Contains(logs.String(), "Concierge check") {
		t.Fatalf("number or text in the log: %s", logs.String())
	}
}

// waiting is what holds emaild's first copy back, so it lasts only while someone polls the check: a
// check sent and left (the dialog closed) stops slowing the copy.
func TestWaitingOnlyWhileTheCheckIsPolled(t *testing.T) {
	g := &rig{ints: []int{0}}
	h, waiting := serve(g, nil)
	do(h, http.MethodPost, "/x/check", `{}`)
	if !waiting() {
		t.Fatal("a check just sent is not waiting")
	}
	g.later = 20 * time.Second
	state(t, h)
	g.later = 49 * time.Second
	if !waiting() {
		t.Fatal("polled 29 s ago: still waiting")
	}
	g.later = 51 * time.Second
	if waiting() {
		t.Fatal("not polled for 31 s: no longer waiting")
	}
	state(t, h)
	if !waiting() {
		t.Fatal("polled again: waiting again")
	}
}
