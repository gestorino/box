package sms

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var question = regexp.MustCompile(`^Concierge check: what is (\d+) \+ (\d+)\?$`)

// answerTo is the sum a sent check asks for.
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

func checkState(t *testing.T, h http.Handler) string {
	t.Helper()
	return strings.TrimSpace(get(h, "/sms/check").Body.String())
}

func TestSMSCheckPassesOnTheReplyFromThatNumber(t *testing.T) {
	g := newRig(t)
	h := NewHandler(g.a, g.m, g.r.Tick, g.ls())
	if rec := post(h, "/sms/check", `{"to":"+351 911 111 111"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(g.m.sent) != 1 || g.m.sent[0][0] != "+351911111111" {
		t.Fatalf("sent %v", g.m.sent)
	}
	answer := answerTo(t, g.m.sent[0][1])
	if s := checkState(t, h); s != `{"state":"waiting"}` {
		t.Fatal(s)
	}
	g.m.msgs = append(g.m.msgs, received("/SMS/9", "+351911111111", answer, time.Now().Add(time.Minute)))
	g.r.Tick(ctx)
	if s := checkState(t, h); s != `{"state":"passed"}` {
		t.Fatal(s)
	}
}

func TestSMSCheckIgnoresOtherNumbersAndOlderMessages(t *testing.T) {
	g := newRig(t)
	h := NewHandler(g.a, g.m, g.r.Tick, g.ls())
	post(h, "/sms/check", `{"to":"+351911111111"}`)
	answer := answerTo(t, g.m.sent[0][1])
	g.m.msgs = append(g.m.msgs,
		received("/SMS/8", "+351922222222", answer, time.Now().Add(time.Minute)),
		received("/SMS/9", "+351911111111", answer, time.Now().Add(-time.Hour)))
	g.r.Tick(ctx)
	if s := checkState(t, h); s != `{"state":"waiting"}` {
		t.Fatal(s)
	}
}

func TestSMSCheckNeedsAnInternationalNumber(t *testing.T) {
	g := newRig(t)
	if rec := post(NewHandler(g.a, g.m, g.r.Tick, g.ls()), "/sms/check", `{"to":"911111111"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
	if len(g.m.sent) != 0 {
		t.Fatalf("sent %v", g.m.sent)
	}
}
