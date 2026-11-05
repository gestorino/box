package calls

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/realtime"
)

func voiceIs(v hostsettings.Voice) func() (hostsettings.Voice, bool) {
	return func() (hostsettings.Voice, bool) { return v, true }
}

func ownerIs(o hostsettings.Owner) func() (hostsettings.Owner, bool) {
	return func() (hostsettings.Owner, bool) { return o, true }
}

const goodCall = `{"number":"+351 912 345 678","language":"pt","opening":"friendly","prompt":"Book a table for two at 8pm."}`

// callRig is a handler whose sessions are tk, recording each SessionSpec, and the modem it dials.
func callRig(t *testing.T, tk *fakeTalker, v func() (hostsettings.Voice, bool), o func() (hostsettings.Owner, bool)) (http.Handler, *dialModem, *[]SessionSpec) {
	t.Helper()
	g := newRig(t)
	c, m, _, _ := converser(t, st("active", "accepted"))
	var specs []SessionSpec
	tw := withKey(t, func(_ context.Context, s SessionSpec) (Talker, error) {
		specs = append(specs, s)
		return tk, nil
	})
	return NewHandler(ctx, g.a, c, tw, v, o), m, &specs
}

func TestPromptCallNeedsAKey(t *testing.T) {
	g := newRig(t)
	c, m, _, _ := converser(t, st("active", "accepted"))
	rec := postJSON(NewHandler(ctx, g.a, c, noKey(t), voiceIs(hostsettings.Voice{}), ownerIs(hostsettings.Owner{FirstName: "N"})), "/calls/call", goodCall)
	if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"no OpenAI key"}` || len(m.log.get()) != 0 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, m.log.get())
	}
}

func TestPromptCallNeedsTheVoiceSettings(t *testing.T) {
	h, m, specs := callRig(t, newTalker(nil), voiceUnknown, ownerUnknown)
	rec := postJSON(h, "/calls/call", goodCall)
	if rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != `{"error":"the Voice settings aren't available yet"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(m.log.get()) != 0 || len(*specs) != 0 {
		t.Fatalf("dialled: %v %v", m.log.get(), *specs)
	}
}

func TestPromptCallNeedsTheOwner(t *testing.T) {
	h, m, _ := callRig(t, newTalker(nil), voiceIs(hostsettings.Voice{Model: "gpt-realtime-2.1"}), ownerUnknown)
	rec := postJSON(h, "/calls/call", goodCall)
	if rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != `{"error":"the Voice settings aren't available yet"}` || len(m.log.get()) != 0 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, m.log.get())
	}
}

func TestPromptCallNeedsAName(t *testing.T) {
	h, m, _ := callRig(t, newTalker(nil), voiceIs(hostsettings.Voice{Model: "gpt-realtime-2.1"}), ownerIs(hostsettings.Owner{}))
	rec := postJSON(h, "/calls/call", goodCall)
	if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"set your name in Account first"}` || len(m.log.get()) != 0 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, m.log.get())
	}
}

func TestPromptCallRefusesBadInput(t *testing.T) {
	h, m, specs := callRig(t, newTalker(nil), voiceIs(hostsettings.Voice{Model: "gpt-realtime-2.1"}), ownerIs(hostsettings.Owner{FirstName: "N"}))
	for name, body := range map[string]string{
		"no prompt":     `{"number":"+351912345678","language":"pt","opening":"formal"}`,
		"blank prompt":  `{"number":"+351912345678","language":"pt","opening":"formal","prompt":"  "}`,
		"long prompt":   `{"number":"+351912345678","language":"pt","opening":"formal","prompt":"` + strings.Repeat("я", maxPrompt+1) + `"}`,
		"bad opening":   `{"number":"+351912345678","language":"pt","opening":"rude","prompt":"x"}`,
		"bad language":  `{"number":"+351912345678","language":"de","opening":"formal","prompt":"x"}`,
		"no language":   `{"number":"+351912345678","opening":"formal","prompt":"x"}`,
		"bad number":    `{"number":"12","language":"pt","opening":"formal","prompt":"x"}`,
		"unknown field": `{"number":"+351912345678","language":"pt","opening":"formal","prompt":"x","to":"y"}`,
	} {
		if rec := postJSON(h, "/calls/call", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if len(m.log.get()) != 0 || len(*specs) != 0 {
		t.Fatalf("dialled: %v %v", m.log.get(), *specs)
	}
}

func TestPromptCallAcceptsAFullCyrillicPrompt(t *testing.T) {
	tk := newTalker(nil)
	tk.tools <- realtime.ToolCall{Name: "end_call", Arguments: `{"result":"done","summary":"ok"}`}
	h, _, _ := callRig(t, tk, voiceIs(hostsettings.Voice{Model: "gpt-realtime-2.1"}), ownerIs(hostsettings.Owner{FirstName: "N"}))
	body := `{"number":"+351912345678","language":"ru","opening":"formal","prompt":"` + strings.Repeat("я", maxPrompt) + `"}`
	if rec := postJSON(h, "/calls/call", body); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestPromptCallOpensALeadSessionOnTheChosenModel(t *testing.T) {
	tk := newTalker(nil)
	tk.tools <- realtime.ToolCall{Name: "end_call", Arguments: `{"result":"needs_you","summary":"They asked for a number."}`}
	v, o := hostsettings.Voice{Model: "gpt-realtime-2.1-mini"}, hostsettings.Owner{FirstName: "Alex"}
	h, m, specs := callRig(t, tk, voiceIs(v), ownerIs(o))
	rec := postJSON(h, "/calls/call", goodCall)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"outcome":"needs_you"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if m.log.get()[0] != "dial +351912345678" || len(*specs) != 1 {
		t.Fatalf("%v %v", m.log.get(), *specs)
	}
	s := (*specs)[0]
	if !s.Lead || s.Model != "gpt-realtime-2.1-mini" || s.Language != "pt" || len(s.Tools) != 1 || s.Tools[0].Name != "end_call" {
		t.Fatalf("%+v", s)
	}
	lang, _ := languageOf("pt")
	if s.Instructions != promptInstructions(lang, "friendly", "Book a table for two at 8pm.", o) {
		t.Fatalf("instructions: %q", s.Instructions)
	}
}

func TestPromptCallWhileBusy(t *testing.T) {
	g := newRig(t)
	c, _, _, _ := converser(t)
	tw := withKey(t, func(context.Context, SessionSpec) (Talker, error) { return newTalker(nil), nil })
	h := NewHandler(ctx, g.a, c, tw, voiceIs(hostsettings.Voice{Model: "gpt-realtime-2.1"}), ownerIs(hostsettings.Owner{FirstName: "N"}))
	c.busy.Lock()
	defer c.busy.Unlock()
	if rec := postJSON(h, "/calls/call", goodCall); rec.Code != http.StatusConflict {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestPromptCallRefusedKey(t *testing.T) {
	g := newRig(t)
	c, m, _, _ := converser(t, st("active", "accepted"))
	tw := withKey(t, func(context.Context, SessionSpec) (Talker, error) { return nil, realtime.ErrRefused })
	h := NewHandler(ctx, g.a, c, tw, voiceIs(hostsettings.Voice{Model: "gpt-realtime-2.1"}), ownerIs(hostsettings.Owner{FirstName: "N"}))
	if rec := postJSON(h, "/calls/call", goodCall); rec.Code != http.StatusBadGateway || len(m.log.get()) != 0 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, m.log.get())
	}
}

// serveCall posts goodCall to h on rctx in the background; the response arrives once the call is over.
func serveCall(h http.Handler, rctx context.Context) <-chan *httptest.ResponseRecorder {
	served := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/calls/call", strings.NewReader(goodCall)).WithContext(rctx))
		served <- rec
	}()
	return served
}

// The call is calld's, not the request's: the console going away mid-call leaves it running.
func TestAPromptCallOutlivesItsRequest(t *testing.T) {
	tk := newTalker(nil)
	h, _, _ := callRig(t, tk, voiceIs(hostsettings.Voice{Model: "gpt-realtime-2.1"}), ownerIs(alex))
	rctx, leave := context.WithCancel(ctx)
	served := serveCall(h, rctx)
	waitFor(t, tk, 1)
	leave()
	time.Sleep(100 * time.Millisecond)
	select {
	case rec := <-served:
		t.Fatalf("the call ended with its request: %s", rec.Body)
	default:
	}
	tk.tools <- realtime.ToolCall{Name: "end_call", Arguments: `{"result":"done","summary":"Booked."}`}
	select {
	case rec := <-served:
		if strings.TrimSpace(rec.Body.String()) != `{"outcome":"done"}` {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("end_call never ended the call")
	}
}

func TestCalldShuttingDownEndsAPromptCall(t *testing.T) {
	defer func(was time.Duration) { hangupGrace = was }(hangupGrace)
	hangupGrace = grace
	tk := newTalker(nil)
	g := newRig(t)
	c, _, _, _ := converser(t, st("active", "accepted"))
	daemon, stop := context.WithCancel(ctx)
	h := NewHandler(daemon, g.a, c, withKey(t, func(context.Context, SessionSpec) (Talker, error) { return tk, nil }), voiceIs(hostsettings.Voice{Model: "gpt-realtime-2.1"}), ownerIs(alex))
	served := serveCall(h, ctx)
	waitFor(t, tk, 1)
	stop()
	select {
	case rec := <-served:
		if strings.TrimSpace(rec.Body.String()) != `{"outcome":"not_done"}` {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the call outlived calld")
	}
}
