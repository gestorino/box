package email

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const owner = "owner@example.org"

var question = regexp.MustCompile(`^Concierge check: what is (\d+) \+ (\d+)\?$`)

func answerTo(t *testing.T, text string) string {
	t.Helper()
	m := question.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("not a check: %q", text)
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return strconv.Itoa(a + b)
}

func TestEmailCheckPassesOnAReplyInItsThread(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	h := NewHandler(g.s, g.a, g.ls())
	// the owner says where the check goes: any address they can reply from
	for _, body := range []string{`{"to":""}`, `{"to":"nope"}`, `{"to":"Owner <owner@example.org>"}`, `{"to":"a@x, b@x"}`} {
		if rec := call(h, "POST", "/email/check", body); rec.Code != http.StatusBadRequest ||
			!strings.Contains(rec.Body.String(), "an email address, like you@example.com") {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := call(h, "POST", "/email/check", `{"to":" Owner@Example.org "}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(g.sent) != 1 || g.sent[0].m.To != owner || g.sent[0].m.Subject != "Concierge check" || g.sent[0].m.MessageID == "" {
		t.Fatalf("%+v", g.sent)
	}
	check := g.sent[0].m
	answer := answerTo(t, check.Text)
	// Gmail files the check and the owner's reply in All Mail; the reply quotes a header with a time.
	g.f.add(g.c.t, rawMail(me, check.MessageID, "", check.Subject, check.Text))
	g.f.add(g.c.t, rawMail(me, "other@x", "", "Unrelated", answer))
	if err := g.s.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if s := strings.TrimSpace(call(h, "GET", "/email/check", "").Body.String()); s != `{"state":"waiting"}` {
		t.Fatalf("an answer outside the thread passed: %s", s)
	}
	g.f.add(g.c.t, rawMail(owner, "r1@x", "<"+check.MessageID+">", "Re: Concierge check",
		answer+"\r\n\r\nOn Wed, 28 Oct 2026 at 14:41 <"+me+"> wrote:\r\n> "+check.Text))
	if err := g.s.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if s := strings.TrimSpace(call(h, "GET", "/email/check", "").Body.String()); s != `{"state":"passed"}` {
		t.Fatal(s)
	}
}

func TestEmailCheckNotConnected(t *testing.T) {
	g := newSrig(t)
	rec := call(NewHandler(g.s, g.a, g.ls()), "POST", "/email/check", `{"to":"owner@example.org"}`)
	if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"not connected"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// A reply that lands while a big first copy runs is fetched on the next pass, not after the copy:
// the copy yields after a batch while a check waits.
func TestCopyYieldsWhileACheckWaits(t *testing.T) {
	for _, checking := range []bool{true, false} {
		g := newSrig(t)
		for i := range 30 {
			g.mail(5, "old"+strconv.Itoa(i))
		}
		g.connect(t, "copy", false)
		g.mail(0, "new")
		g.s.SetChecking(func() bool { return checking })
		if err := g.s.syncOnce(ctx); err != nil {
			t.Fatal(err)
		}
		want, state := 31, "connected"
		if checking {
			want, state = 11, "copying" // the new mail, then one batch of ten
		}
		if n := g.count(t); n != want || g.s.Status().State != state {
			t.Fatalf("checking %v: %d stored, %s", checking, n, g.s.Status().State)
		}
	}
}
