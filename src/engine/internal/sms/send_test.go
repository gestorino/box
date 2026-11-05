package sms

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSendSMS(t *testing.T) {
	g := newRig(t)
	rec := post(NewHandler(g.a, g.m, g.r.Tick, g.ls()), "/sms/send", `{"number":"+351 912 345 678","text":"hi"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if want := [][2]string{{"+351912345678", "hi"}}; !reflect.DeepEqual(g.m.sent, want) {
		t.Fatalf("sent %v", g.m.sent)
	}
}

func TestSendSMSBadInput(t *testing.T) {
	g := newRig(t)
	h := NewHandler(g.a, g.m, g.r.Tick, g.ls())
	for _, body := range []string{
		`{"number":"12","text":"hi"}`,
		`{"number":"abc","text":"hi"}`,
		`{"number":"+351912345678","text":""}`,
		`{"number":"+351912345678","text":"` + strings.Repeat("a", 1601) + `"}`,
		`{"number":"+351912345678","text":"hi","to":"x"}`,
	} {
		if rec := post(h, "/sms/send", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%.60s: %d", body, rec.Code)
		}
	}
	if len(g.m.sent) != 0 {
		t.Fatalf("sent %v", g.m.sent)
	}
}

func TestSendSMSModemFails(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	g := newRig(t)
	g.m.sendErr = errors.New("org.freedesktop.ModemManager1.Error.Core.Failed")
	rec := post(NewHandler(g.a, g.m, g.r.Tick, g.ls()), "/sms/send", `{"number":"+351912345678","text":"secret words"}`)
	if rec.Code != http.StatusBadGateway || strings.TrimSpace(rec.Body.String()) != `{"error":"SMS not sent"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if strings.Contains(logs.String(), "351912345678") || strings.Contains(logs.String(), "secret") {
		t.Fatalf("logged the number or the text: %s", logs.String())
	}
}

// The send answers once the message is in the archive, so the thread shows it as soon as it reloads.
func TestSentSMSIsInTheThreadWhenSendAnswers(t *testing.T) {
	g := newRig(t)
	if rec := post(NewHandler(g.a, g.m, g.r.Tick, g.ls()), "/sms/send", `{"number":"+351912345678","text":"on my way"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	th, err := g.a.Thread(ctx, "+351912345678")
	if err != nil || len(th) != 1 || th[0].Direction != "out" || th[0].Text != "on my way" {
		t.Fatalf("%+v %v", th, err)
	}
}

// Run's ticks and a send's tick may overlap.
func TestTicksMayOverlap(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "+351911111111", "hello", t0)}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); g.r.Tick(ctx) }()
	}
	wg.Wait()
	if th, _ := g.a.Thread(ctx, "+351911111111"); len(th) != 1 {
		t.Fatalf("%+v", th)
	}
}
