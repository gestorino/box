package sms

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHandlerReads(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "+351911111111", "hello", t0), received("/SMS/2", "WhatsApp", "code 123-456", t0.Add(time.Minute))}
	g.r.Tick(ctx)
	h := NewHandler(g.a, g.m, g.r.Tick, g.ls())

	var conv []Conversation
	json.Unmarshal(get(h, "/sms/conversations").Body.Bytes(), &conv)
	if len(conv) != 2 || conv[0].Number != "WhatsApp" {
		t.Fatalf("conversations %+v", conv)
	}
	var th []Message
	json.Unmarshal(get(h, "/sms/thread?number=%2B351911111111").Body.Bytes(), &th)
	if len(th) != 1 || th[0].Text != "hello" {
		t.Fatalf("thread %+v", th)
	}
	if rec := get(h, "/sms/thread"); rec.Code != http.StatusBadRequest {
		t.Fatalf("thread without number: %d", rec.Code)
	}
	if rec := get(h, "/sms/thread?number="); rec.Code != http.StatusOK {
		t.Fatalf("the unknown sender (empty number) is a valid thread: %d", rec.Code)
	}
	var recent []Message
	json.Unmarshal(get(h, "/sms/recent?minutes=10").Body.Bytes(), &recent)
	if len(recent) != 2 || recent[0].Number != "WhatsApp" {
		t.Fatalf("recent %+v", recent)
	}
	if rec := get(h, "/sms/recent?minutes=99999"); rec.Code != http.StatusBadRequest {
		t.Fatalf("recent beyond a day: %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sms/conversations", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("read-only API answered POST with %d", rec.Code)
	}
}

func TestHandlerErrorsNeverLogText(t *testing.T) {
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "X", "secret code 4242", t0)}
	g.r.Tick(ctx)
	g.a.Close()
	if rec := get(NewHandler(g.a, g.m, g.r.Tick, g.ls()), "/sms/thread?number=X"); rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d", rec.Code)
	}
	if strings.Contains(logs.String(), "4242") {
		t.Fatalf("message text in the log: %s", logs.String())
	}
}

func TestSMSTimeNormalisesToUTC(t *testing.T) {
	for in, want := range map[string]string{
		"2026-10-27T12:00:00+00":    "2026-10-27T12:00:00Z",
		"2026-10-27T12:00:00+01:00": "2026-10-27T11:00:00Z",
		"":                          "",
		"garbage":                   "garbage",
	} {
		if got := smsTime(in); got != want {
			t.Errorf("smsTime(%q) = %q, want %q", in, got, want)
		}
	}
}

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func TestRemoveKeepLeavesArchive(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "+351911111111", "hello", t0)}
	g.r.Tick(ctx)
	h := NewHandler(g.a, g.m, g.r.Tick, g.ls())
	if rec := post(h, "/sms/remove", `{"delete_data":false,"force":false}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if c, _ := g.a.Conversations(ctx); len(c) != 1 {
		t.Fatalf("kept archive lost: %+v", c)
	}
}

func TestRemoveDeleteWipes(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "+351911111111", "hello", t0), received("/SMS/2", "MEO", "hi", t0)}
	g.r.Tick(ctx)
	h := NewHandler(g.a, g.m, g.r.Tick, g.ls())
	for range 2 { // a retried remove still succeeds
		if rec := post(h, "/sms/remove", `{"delete_data":true,"force":false}`); rec.Code != http.StatusNoContent {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	if c, _ := g.a.Conversations(ctx); len(c) != 0 {
		t.Fatalf("not wiped: %+v", c)
	}
	for _, bad := range []string{`{"delete_data":"yes"}`, `{"delete_data":true,"extra":1}`, `nope`} {
		if rec := post(h, "/sms/remove", bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
}
