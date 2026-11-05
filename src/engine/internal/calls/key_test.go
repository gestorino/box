package calls

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/secrets"
	"concierge/engine/internal/secrets/secretstest"
)

const testKey = "sk-proj-0123456789abcdefc3d4"

// noKey is calld before the owner set a key: a secretd with nothing in it, and no OpenAI to ask.
func noKey(t *testing.T) OpenAI {
	t.Helper()
	return OpenAI{Keys: secretstest.Start(t, "calld")}
}

// keys is calld's secretd as a test reads and writes its OpenAI key.
type keys struct{ c *secrets.Client }

func kv(c *secrets.Client) keys { return keys{c} }

func (k keys) Set(v string) error { return k.c.Put(context.Background(), "openai", v) }

func (k keys) Get() (string, bool, error) { return k.c.Get(context.Background(), "openai") }

func (k keys) Present() bool { _, ok, err := k.Get(); return ok && err == nil }

// withKey is calld holding testKey, its sessions opened by open.
func withKey(t *testing.T, open func(context.Context, SessionSpec) (Talker, error)) OpenAI {
	t.Helper()
	ai := noKey(t)
	if err := kv(ai.Keys).Set(testKey); err != nil {
		t.Fatal(err)
	}
	ai.Open = func(ctx context.Context, _ string, s SessionSpec) (Talker, error) { return open(ctx, s) }
	return ai
}

// fakeOpenAI answers GET /v1/models: 200 for testKey, 401 for any other key, or status when set.
type fakeOpenAI struct {
	mu     sync.Mutex
	status int
	saw    []string // method, path and Authorization of each request
}

func (f *fakeOpenAI) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.saw = append(f.saw, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		status := f.status
		f.mu.Unlock()
		switch {
		case status != 0:
			w.WriteHeader(status)
		case r.Header.Get("Authorization") != "Bearer "+testKey:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"Incorrect API key provided","code":"invalid_api_key"}}`))
		default:
			w.Write([]byte(`{"object":"list","data":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeOpenAI) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.saw...)
}

// keyRig is calld with no key yet, checking keys with the fake OpenAI behind srv.
func keyRig(t *testing.T, srv *httptest.Server) (http.Handler, keys) {
	t.Helper()
	ai := noKey(t)
	ai.Checker = NewKeyChecker()
	ai.Checker.BaseURL = srv.URL
	return NewHandler(ctx, newRig(t).a, nil, ai, voiceUnknown, ownerUnknown), kv(ai.Keys)
}

func putKey(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/calls/key", strings.NewReader(body)))
	return rec
}

func TestGetKeySaysWhetherOneIsSetNeverTheKey(t *testing.T) {
	h, kk := keyRig(t, (&fakeOpenAI{}).server(t))
	if rec := get(h, "/calls/key"); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"set":false,"masked":""}` {
		t.Fatalf("unset: %d %s", rec.Code, rec.Body)
	}
	if err := kk.Set(testKey); err != nil {
		t.Fatal(err)
	}
	if rec := get(h, "/calls/key"); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"set":true,"masked":"sk-…c3d4"}` {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
}

func TestPutKeyStoresOnlyAKeyOpenAIAccepts(t *testing.T) {
	f := &fakeOpenAI{}
	h, kk := keyRig(t, f.server(t))
	rec := putKey(h, `{"key":"sk-proj-wrong"}`)
	if rec.Code != http.StatusUnprocessableEntity || strings.TrimSpace(rec.Body.String()) != `{"error":"OpenAI refused this key"}` {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	if kk.Present() {
		t.Fatal("a refused key was stored")
	}
	if rec := putKey(h, `{"key":"  `+testKey+`\n"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("accepted: %d %s", rec.Code, rec.Body)
	}
	if got, _, _ := kk.Get(); got != testKey {
		t.Fatalf("stored %q, want the trimmed key", got)
	}
	if got := f.requests(); len(got) != 2 || got[1] != "GET /v1/models Bearer "+testKey {
		t.Fatalf("OpenAI saw %q", got)
	}
	f.status = http.StatusForbidden
	if rec := putKey(h, `{"key":"sk-proj-revoked-0000"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("403: %d %s", rec.Code, rec.Body)
	}
	if got, _, _ := kk.Get(); got != testKey {
		t.Fatalf("a refused key replaced the stored one: %q", got)
	}
}

func TestPutKeyStoresNothingWhenOpenAICannotBeReached(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusNotFound} {
		h, kk := keyRig(t, (&fakeOpenAI{status: status}).server(t))
		rec := putKey(h, `{"key":"`+testKey+`"}`)
		if rec.Code != http.StatusBadGateway || strings.TrimSpace(rec.Body.String()) != `{"error":"couldn't reach OpenAI"}` {
			t.Fatalf("%d: %d %s", status, rec.Code, rec.Body)
		}
		if kk.Present() {
			t.Fatalf("%d: an unchecked key was stored", status)
		}
	}
	srv := (&fakeOpenAI{}).server(t)
	h, kk := keyRig(t, srv)
	srv.Close()
	if rec := putKey(h, `{"key":"`+testKey+`"}`); rec.Code != http.StatusBadGateway || kk.Present() {
		t.Fatalf("closed: %d %s", rec.Code, rec.Body)
	}
}

func TestTheKeyCheckGivesUpOnASilentOpenAI(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(slow.Close)
	c := NewKeyChecker()
	if c.Client.Timeout != 10*time.Second || c.BaseURL != "https://api.openai.com" {
		t.Fatalf("%v %q", c.Client.Timeout, c.BaseURL)
	}
	c.BaseURL, c.Client.Timeout = slow.URL, 50*time.Millisecond
	err := c.Check(context.Background(), testKey)
	if !errors.Is(err, ErrOpenAIUnreachable) || strings.Contains(err.Error(), testKey) {
		t.Fatalf("%v", err)
	}
}

func TestPutKeyRefusesAMalformedBody(t *testing.T) {
	f := &fakeOpenAI{}
	h, kk := keyRig(t, f.server(t))
	for _, body := range []string{``, `{}`, `{"key":"   "}`, `{"key":1}`, `{"key":"sk a"}`, `{"key":"sk\u0007a"}`,
		`{"key":"` + strings.Repeat("a", 513) + `"}`, `{"key":"x","other":1}`, `nope`} {
		if rec := putKey(h, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: %d %s", body, rec.Code, rec.Body)
		}
	}
	if len(f.requests()) != 0 || kk.Present() {
		t.Fatalf("a malformed key went to OpenAI: %q", f.requests())
	}
}

func TestTheKeyIsNeverLogged(t *testing.T) {
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	for _, status := range []int{0, http.StatusUnauthorized, http.StatusBadGateway} {
		h, _ := keyRig(t, (&fakeOpenAI{status: status}).server(t))
		putKey(h, `{"key":"`+testKey+`"}`)
	}
	if strings.Contains(logs.String(), "0123456789abcdef") {
		t.Fatalf("logged: %s", logs.String())
	}
}

// Each call reads the key as it starts: a replaced key is the next call's, with no restart.
func TestEveryCallOpensWithTheKeyStoredNow(t *testing.T) {
	g := newRig(t)
	c, _, _, _ := converser(t, st("active", "accepted"))
	var seen []string
	ai := noKey(t)
	ai.Open = func(_ context.Context, key string, _ SessionSpec) (Talker, error) {
		seen = append(seen, key)
		return newTalker(map[int]string{0: "1", 1: "2"}), nil
	}
	h := NewHandler(ctx, g.a, c, ai, voiceUnknown, ownerUnknown)
	for _, key := range []string{testKey, "sk-proj-replaced-9999"} {
		if err := kv(ai.Keys).Set(key); err != nil {
			t.Fatal(err)
		}
		postJSON(h, "/calls/check", `{"number":"+351912345678"}`)
	}
	if len(seen) != 2 || seen[0] != testKey || seen[1] != "sk-proj-replaced-9999" {
		t.Fatalf("%q", seen)
	}
}

func TestPromptCallOpensWithTheStoredKey(t *testing.T) {
	g := newRig(t)
	c, _, _, _ := converser(t, st("active", "accepted"))
	var seen []string
	ai := noKey(t)
	kv(ai.Keys).Set(testKey)
	ai.Open = func(_ context.Context, key string, _ SessionSpec) (Talker, error) {
		seen = append(seen, key)
		return nil, errors.New("no session in this test")
	}
	postJSON(NewHandler(ctx, g.a, c, ai, voiceIs(hostsettings.Voice{Model: "gpt-realtime-2.1"}), ownerIs(alex)), "/calls/call", goodCall)
	if len(seen) != 1 || seen[0] != testKey {
		t.Fatalf("%q", seen)
	}
}

// Removing Voice forgets the key, whether the history is kept or wiped.
func TestRemoveForgetsTheKey(t *testing.T) {
	for _, body := range []string{`{"delete_data":false,"force":false}`, `{"delete_data":true,"force":false}`} {
		g := newRig(t)
		ai := withKey(t, nil)
		if code := removeCalls(NewHandler(ctx, g.a, nil, ai, voiceUnknown, ownerUnknown), body); code != http.StatusNoContent {
			t.Fatalf("%s: %d", body, code)
		}
		if kv(ai.Keys).Present() {
			t.Fatalf("%s: the key outlived Voice", body)
		}
	}
}

// Any secretd fault is "secrets unavailable" (502), never "no OpenAI key" (409, an absent key).
func TestKeyRoutesRefuseWhenSecretsUnavailable(t *testing.T) {
	g := newRig(t)
	c, _, _, _ := converser(t, st("active", "accepted"))
	ai := OpenAI{Keys: secretstest.Down(t), Checker: NewKeyChecker()}
	srv := (&fakeOpenAI{}).server(t)
	ai.Checker.BaseURL = srv.URL
	h := NewHandler(ctx, g.a, c, ai, voiceIs(hostsettings.Voice{Model: "gpt-realtime-2.1"}), ownerIs(alex))
	const want = `{"error":"secrets unavailable"}`
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"GET key":    get(h, "/calls/key"),
		"PUT key":    putKey(h, `{"key":"`+testKey+`"}`),
		"POST check": postJSON(h, "/calls/check", `{"number":"+351912345678"}`),
		"POST call":  postJSON(h, "/calls/call", goodCall),
	} {
		if rec.Code != http.StatusBadGateway || strings.TrimSpace(rec.Body.String()) != want {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

// Removing Voice never goes past a key secretd couldn't delete, forced or not: 502 "secrets
// unavailable" and nothing changes (the history stays), for the owner to retry.
func TestRemoveWithSecretsUnavailableIs502AndChangesNothing(t *testing.T) {
	g := newRig(t)
	g.a.Start(ctx, "+351911111111", "incoming", t0)
	h := NewHandler(ctx, g.a, nil, OpenAI{Keys: secretstest.Down(t)}, voiceUnknown, ownerUnknown)
	for _, body := range []string{`{"delete_data":true,"force":false}`, `{"delete_data":true,"force":true}`} {
		rec := postJSON(h, "/calls/remove", body)
		if rec.Code != http.StatusBadGateway || strings.TrimSpace(rec.Body.String()) != `{"error":"secrets unavailable"}` {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if calls, _ := g.a.Calls(ctx, 10); len(calls) != 1 {
		t.Fatalf("history changed: %+v", calls)
	}
}
