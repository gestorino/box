package calls

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/realtime"
)

// The check follows the Voice model setting, and the model never leads it.
func TestTheCheckUsesTheVoiceModel(t *testing.T) {
	g := newRig(t)
	c, _, _, _ := converser(t, st("active", "accepted"))
	var got []SessionSpec
	tw := withKey(t, func(_ context.Context, s SessionSpec) (Talker, error) {
		got = append(got, s)
		return newTalker(map[int]string{0: "1", 1: "2"}), nil
	})
	voice := func() (hostsettings.Voice, bool) { return hostsettings.Voice{Model: "gpt-realtime-2"}, true }
	postJSON(NewHandler(ctx, g.a, c, tw, voice, ownerUnknown), "/calls/check", `{"number":"+351912345678"}`)
	postJSON(NewHandler(ctx, g.a, c, tw, voiceUnknown, ownerUnknown), "/calls/check", `{"number":"+351912345678"}`)
	if len(got) != 2 || got[0].Model != "gpt-realtime-2" || got[1].Model != hostsettings.VoiceModels[0] || got[0].Lead || got[1].Lead {
		t.Fatalf("%+v", got)
	}
}

func ownerUnknown() (hostsettings.Owner, bool) { return hostsettings.Owner{}, false }

func voiceUnknown() (hostsettings.Voice, bool) { return hostsettings.Voice{}, false }

func checkState(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/calls/check", nil))
	return strings.TrimSpace(rec.Body.String())
}

// Without a key there is no check: nothing is dialled and nothing is armed.
func TestCheckNeedsAKey(t *testing.T) {
	g := newRig(t)
	c, m, _, _ := converser(t, st("active", "accepted"))
	h := NewHandler(ctx, g.a, c, noKey(t), voiceUnknown, ownerUnknown)
	rec := postJSON(h, "/calls/check", `{"number":"+351912345678"}`)
	if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"no OpenAI key"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(m.log.get()) != 0 {
		t.Fatalf("dialled: %v", m.log.get())
	}
	if s := checkState(t, h); s != `{"state":"none"}` {
		t.Fatal(s)
	}
}

// checkSessions is calld holding a key, its sessions tk, recording the instructions each was opened with
// and, after a "|", its language.
func checkSessions(t *testing.T, tk *fakeTalker, opened *[]string) OpenAI {
	return withKey(t, func(_ context.Context, s SessionSpec) (Talker, error) {
		*opened = append(*opened, s.Instructions+"|"+s.Language)
		return tk, nil
	})
}

func TestTheCheckPassesOnTheSpokenSum(t *testing.T) {
	g := newRig(t)
	c, m, _, _ := converser(t, st("active", "accepted"))
	tk := newTalker(nil)
	var opened []string
	h := NewHandler(ctx, g.a, c, checkSessions(t, tk, &opened), voiceUnknown, ownerUnknown)
	// The owner answers whatever sum the first question asks.
	go func() {
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
			if said := tk.saidSoFar(); len(said) > 0 {
				tk.heard <- sumAsked(t, said[0])
				return
			}
		}
	}()
	rec := postJSON(h, "/calls/check", `{"number":"+351 912 345 678","language":"pt"}`)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"outcome":"passed"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if m.log.get()[0] != "dial +351912345678" || len(opened) != 1 || opened[0] != CheckInstructions+"|pt" {
		t.Fatalf("%v %q", m.log.get(), opened)
	}
	if !strings.Contains(tk.saidSoFar()[0], "Portuguese") {
		t.Fatalf("%q", tk.saidSoFar()[0])
	}
	if s := checkState(t, h); s != `{"state":"passed"}` {
		t.Fatal(s)
	}
}

// asked finds the question's two numbers in any language: "… 23 plus 19?", "… 23 mais 19?", "… 23 плюс 19?".
var asked = regexp.MustCompile(`(\d+) \pL+ (\d+)\?`)

func sumAsked(t *testing.T, instructions string) string {
	m := asked.FindStringSubmatch(instructions)
	if m == nil {
		t.Errorf("no question in %q", instructions)
		return ""
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return strconv.Itoa(a + b)
}

func TestAWrongSumIsNotPassed(t *testing.T) {
	g := newRig(t)
	c, _, _, _ := converser(t, st("active", "accepted"))
	tk := newTalker(map[int]string{0: "1", 1: "2"}) // never the sum: A, B ≥ 10
	var opened []string
	h := NewHandler(ctx, g.a, c, checkSessions(t, tk, &opened), voiceUnknown, ownerUnknown)
	rec := postJSON(h, "/calls/check", `{"number":"+351912345678"}`)
	if strings.TrimSpace(rec.Body.String()) != `{"outcome":"wrong"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(tk.saidSoFar()[0], "English") { // the default language
		t.Fatalf("%q", tk.saidSoFar()[0])
	}
	if s := checkState(t, h); s != `{"state":"none"}` {
		t.Fatal(s)
	}
}

func TestACheckOnARefusedKeyNeverDials(t *testing.T) {
	g := newRig(t)
	c, m, _, _ := converser(t, st("active", "accepted"))
	h := NewHandler(ctx, g.a, c, withKey(t, func(context.Context, SessionSpec) (Talker, error) { return nil, realtime.ErrRefused }), voiceUnknown, ownerUnknown)
	rec := postJSON(h, "/calls/check", `{"number":"+351912345678","language":"en"}`)
	if rec.Code != http.StatusBadGateway || strings.TrimSpace(rec.Body.String()) != `{"error":"OpenAI refused the key"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(m.log.get()) != 0 {
		t.Fatalf("dialled: %v", m.log.get())
	}
}

// A dead uplink at the start never leaves the check hanging: opening the session gives up within
// openWait, and nothing is dialled.
func TestACheckWhoseOpenHangsGivesUp(t *testing.T) {
	prev := openWait
	openWait = 50 * time.Millisecond
	t.Cleanup(func() { openWait = prev })
	g := newRig(t)
	c, m, _, _ := converser(t, st("active", "accepted"))
	h := NewHandler(ctx, g.a, c, withKey(t, func(ctx context.Context, _ SessionSpec) (Talker, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}), voiceUnknown, ownerUnknown)
	within(t, time.Second, func() {
		rec := postJSON(h, "/calls/check", `{"number":"+351912345678"}`)
		if rec.Code != http.StatusBadGateway || strings.TrimSpace(rec.Body.String()) != `{"error":"call not placed"}` {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
	if len(m.log.get()) != 0 {
		t.Fatalf("dialled: %v", m.log.get())
	}
}

func TestCheckBadLanguage(t *testing.T) {
	g := newRig(t)
	c, m, _, _ := converser(t, st("active", "accepted"))
	var opened []string
	h := NewHandler(ctx, g.a, c, checkSessions(t, newTalker(nil), &opened), voiceUnknown, ownerUnknown)
	for _, body := range []string{`{"number":"+351912345678","language":"fr"}`, `{"number":"+351912345678","language":""}`} {
		if rec := postJSON(h, "/calls/check", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, rec.Code)
		}
	}
	if len(m.log.get()) != 0 || len(opened) != 0 {
		t.Fatalf("%v %v", m.log.get(), opened)
	}
}

func TestCheckNeverLogsTheTranscript(t *testing.T) {
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	g := newRig(t)
	c, _, _, _ := converser(t, st("active", "accepted"))
	tk := newTalker(map[int]string{0: "forty one", 1: "forty three"})
	var opened []string
	postJSON(NewHandler(ctx, g.a, c, checkSessions(t, tk, &opened), voiceUnknown, ownerUnknown), "/calls/check", `{"number":"+351912345678"}`)
	if strings.Contains(logs.String(), "912345678") || strings.Contains(logs.String(), "forty") {
		t.Fatalf("logged: %s", logs.String())
	}
}

func TestVoiceCheckBadInput(t *testing.T) {
	g := newRig(t)
	c, m, _, _ := converser(t, st("active", "accepted"))
	var opened []string
	h := NewHandler(ctx, g.a, c, checkSessions(t, newTalker(nil), &opened), voiceUnknown, ownerUnknown)
	for _, body := range []string{`{"number":"912345678"}`, `{"number":"+35"}`, `{}`, `{"number":"+351912345678","x":1}`} {
		if rec := postJSON(h, "/calls/check", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, rec.Code)
		}
	}
	if len(m.log.get()) != 0 || len(opened) != 0 {
		t.Fatalf("dialled: %v %v", m.log.get(), opened)
	}
}
