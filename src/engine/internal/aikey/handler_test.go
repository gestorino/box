package aikey

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"concierge/engine/internal/secrets"
	"concierge/engine/internal/secrets/secretstest"
)

func newHandler(t *testing.T, f *fakeOpenRouter) (http.Handler, keys) {
	t.Helper()
	s := keys{secretstest.Start(t, "engine")}
	return NewHandler(s.c, checker(f.server(t))), s
}

// keys is the engine's secretd as a test reads and writes its OpenRouter key.
type keys struct{ c *secrets.Client }

func (k keys) Set(v string) error { return k.c.Put(context.Background(), "openrouter", v) }

func (k keys) Get() (string, bool, error) { return k.c.Get(context.Background(), "openrouter") }

func (k keys) Present() bool { _, ok, err := k.Get(); return ok && err == nil }

func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func decoded(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestGetSaysWhetherAKeyIsSetNeverTheKey(t *testing.T) {
	h, s := newHandler(t, &fakeOpenRouter{})
	rec := call(h, "GET", "/api/v1/ai", "")
	if rec.Code != 200 || rec.Body.String() != `{"set":false,"masked":""}`+"\n" {
		t.Fatalf("unset: %d %s", rec.Code, rec.Body)
	}
	if err := s.Set(testKey); err != nil {
		t.Fatal(err)
	}
	rec = call(h, "GET", "/api/v1/ai", "")
	if rec.Code != 200 || rec.Body.String() != `{"set":true,"masked":"sk-or-…a1b2"}`+"\n" {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
}

func TestPutStoresOnlyAKeyOpenRouterAccepts(t *testing.T) {
	h, s := newHandler(t, &fakeOpenRouter{limitRemaining: "1"})
	rec := call(h, "PUT", "/api/v1/ai/key", `{"key":"sk-or-v1-wrong"}`)
	if rec.Code != http.StatusUnprocessableEntity || decoded(t, rec)["error"] != "OpenRouter refused this key" {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	if s.Present() {
		t.Fatal("a refused key was stored")
	}
	rec = call(h, "PUT", "/api/v1/ai/key", `{"key":"  `+testKey+`\n"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("accepted: %d %s", rec.Code, rec.Body)
	}
	if got, _, _ := s.Get(); got != testKey {
		t.Fatalf("stored %q, want the trimmed key", got)
	}
}

func TestPutStoresNothingWhenOpenRouterCannotBeReached(t *testing.T) {
	h, s := newHandler(t, &fakeOpenRouter{keyStatus: http.StatusServiceUnavailable})
	rec := call(h, "PUT", "/api/v1/ai/key", `{"key":"`+testKey+`"}`)
	if rec.Code != http.StatusBadGateway || decoded(t, rec)["error"] != "Couldn't reach OpenRouter" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if s.Present() {
		t.Fatal("an unchecked key was stored")
	}
}

func TestPutRefusesAnEmptyOrMalformedBody(t *testing.T) {
	f := &fakeOpenRouter{}
	h, _ := newHandler(t, f)
	for _, body := range []string{``, `{}`, `{"key":"   "}`, `{"key":1}`, `{"key":"sk-or a"}`, `{"key":"x","other":1}`, `nope`} {
		if rec := call(h, "PUT", "/api/v1/ai/key", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: %d %s", body, rec.Code, rec.Body)
		}
	}
	if len(f.sawAuth) != 0 {
		t.Fatal("a malformed key was sent to OpenRouter")
	}
}

func TestDeleteRemovesTheKey(t *testing.T) {
	h, s := newHandler(t, &fakeOpenRouter{})
	if rec := call(h, "DELETE", "/api/v1/ai/key", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("none stored: %d", rec.Code)
	}
	if err := s.Set(testKey); err != nil {
		t.Fatal(err)
	}
	if rec := call(h, "DELETE", "/api/v1/ai/key", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("stored: %d", rec.Code)
	}
	if s.Present() {
		t.Fatal("key still stored")
	}
}

func TestCheckAsksOpenRouterAboutTheStoredKey(t *testing.T) {
	f := &fakeOpenRouter{limitRemaining: "12.4"}
	h, s := newHandler(t, f)
	rec := call(h, "POST", "/api/v1/ai/check", "")
	if rec.Code != http.StatusConflict || decoded(t, rec)["error"] != "no OpenRouter key" {
		t.Fatalf("none stored: %d %s", rec.Code, rec.Body)
	}
	if err := s.Set(testKey); err != nil {
		t.Fatal(err)
	}
	rec = call(h, "POST", "/api/v1/ai/check", "")
	if rec.Code != 200 || rec.Body.String() != `{"ok":true,"credit_usd":12.4}`+"\n" {
		t.Fatalf("works: %d %s", rec.Code, rec.Body)
	}
	f.limitRemaining, f.creditsStatus = "null", http.StatusForbidden
	rec = call(h, "POST", "/api/v1/ai/check", "")
	if rec.Code != 200 || rec.Body.String() != `{"ok":true}`+"\n" {
		t.Fatalf("credit unknown: %d %s", rec.Code, rec.Body)
	}
	if err := s.Set("sk-or-v1-revoked"); err != nil {
		t.Fatal(err)
	}
	rec = call(h, "POST", "/api/v1/ai/check", "")
	if rec.Code != 200 || rec.Body.String() != `{"ok":false,"error":"OpenRouter refused this key"}`+"\n" {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	if err := s.Set(testKey); err != nil {
		t.Fatal(err)
	}
	f.keyStatus = http.StatusInternalServerError
	rec = call(h, "POST", "/api/v1/ai/check", "")
	if rec.Code != http.StatusBadGateway || decoded(t, rec)["error"] != "Couldn't reach OpenRouter" {
		t.Fatalf("unreachable: %d %s", rec.Code, rec.Body)
	}
}

// Key is what the engine hands the worker: the key the AI page kept, "" when there is none.
func TestKeyIsTheKeptKey(t *testing.T) {
	h, s := newHandler(t, &fakeOpenRouter{limitRemaining: "1"})
	if k, err := Key(context.Background(), s.c); k != "" || err != nil {
		t.Fatalf("none kept: %q %v", k, err)
	}
	if rec := call(h, "PUT", "/api/v1/ai/key", `{"key":"`+testKey+`"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if k, err := Key(context.Background(), s.c); k != testKey || err != nil {
		t.Fatalf("kept: %q %v", k, err)
	}
	if _, err := Key(context.Background(), secretstest.Down(t)); err == nil || err.Error() != "secrets unavailable" {
		t.Fatalf("down: %v", err)
	}
}

// secretd down: every route that touches the key says so, in those words.
func TestSecretsUnavailableAnswers502(t *testing.T) {
	f := &fakeOpenRouter{limitRemaining: "1"}
	h := NewHandler(secretstest.Down(t), checker(f.server(t)))
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/ai", ""},
		{"PUT", "/api/v1/ai/key", `{"key":"` + testKey + `"}`},
		{"DELETE", "/api/v1/ai/key", ""},
		{"POST", "/api/v1/ai/check", ""},
	} {
		rec := call(h, c.method, c.path, c.body)
		if rec.Code != http.StatusBadGateway || rec.Body.String() != `{"error":"secrets unavailable"}`+"\n" {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}
