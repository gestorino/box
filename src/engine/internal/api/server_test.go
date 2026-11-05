package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"concierge/engine/internal/browser"
	"concierge/engine/internal/channels"
)

type fakeBrowser browser.Status

func (f fakeBrowser) Status(context.Context) browser.Status { return browser.Status(f) }

type stubChannel string

func (s stubChannel) Name() string                                 { return string(s) }
func (s stubChannel) Send(context.Context, channels.Message) error { return nil }

func newServer(t *testing.T, b Browser, uiDir string) http.Handler {
	t.Helper()
	reg, err := channels.NewRegistry(stubChannel("email"))
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Login: testLogin, Build: Build{Version: "1.2.3", Revision: "abc1234"}, Browser: b, Channels: reg, UIDir: uiDir})
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.SetBasicAuth("helper", "helper")
	h.ServeHTTP(rec, r)
	return rec
}

func TestHealthz(t *testing.T) {
	rec := get(t, newServer(t, nil, ""), "/healthz")
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestStatusReportsBrowserAndChannels(t *testing.T) {
	b := fakeBrowser{Connected: true, WorkerVersion: "0.1.0", UptimeMS: 42}
	rec := get(t, newServer(t, b, ""), "/api/v1/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Browser.Enabled || !body.Browser.Connected || body.Browser.WorkerVersion != "0.1.0" {
		t.Fatalf("browser %+v", body.Browser)
	}
	if len(body.Channels) != 1 || body.Channels[0] != "email" {
		t.Fatalf("channels %v", body.Channels)
	}
	if body.Version != "1.2.3" || body.Revision != "abc1234" {
		t.Fatalf("build %q %q, want the version and revision the engine was built with", body.Version, body.Revision)
	}
}

func TestStatusWithBrowserDisabled(t *testing.T) {
	rec := get(t, newServer(t, nil, ""), "/api/v1/status")
	var body StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Browser.Enabled || body.Browser.Connected {
		t.Fatalf("browser %+v", body.Browser)
	}
}

func TestServesUIWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>ui</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := get(t, newServer(t, nil, dir), "/")
	if rec.Code != http.StatusOK || rec.Body.String() != "<h1>ui</h1>" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

// The UI routes in the browser (/flows, /logs …), so any path that is not a file gets index.html —
// but a missing asset or an unknown API path stays a 404, never a page that hides the mistake.
func TestUIRoutesFallBackToIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index.html": "<h1>ui</h1>", "assets/app.js": "js"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := newServer(t, nil, dir)

	// A route may hold a dot: a site's key (/accounts/example.com) is a route, not a file.
	for path, want := range map[string]string{"/flows": "<h1>ui</h1>", "/connections/email": "<h1>ui</h1>", "/assets/app.js": "js",
		"/accounts/example.com": "<h1>ui</h1>", "/accounts/example.com/runs/5": "<h1>ui</h1>"} {
		if rec := get(t, h, path); rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s: got %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	for _, path := range []string{"/assets/missing.js", "/missing.svg", "/api/v1/nope"} {
		if rec := get(t, h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, rec.Code)
		}
	}
}

// A deploy must show at once: the page is revalidated on every load, so it never points at the
// previous build's bundle; the bundle's files carry their content hash in the name, so they never change.
func TestUICaching(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "assets/index-AbC123.js", "favicon.svg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := newServer(t, nil, dir)
	for path, want := range map[string]string{
		"/":                       "no-cache",
		"/sign-ins/booksy":        "no-cache",
		"/favicon.svg":            "no-cache",
		"/assets/index-AbC123.js": "private, max-age=31536000, immutable",
	} {
		if got := get(t, h, path).Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", path, got, want)
		}
	}
	// A missing file is never remembered for a year.
	if rec := get(t, h, "/assets/missing.js"); rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "" {
		t.Errorf("missing asset: %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

func TestNoUIIsNotFound(t *testing.T) {
	if rec := get(t, newServer(t, nil, ""), "/"); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestRunsAreBehindTheLogin(t *testing.T) {
	reg, _ := channels.NewRegistry(stubChannel("email"))
	h := New(Config{Login: testLogin, Channels: reg, Runs: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Method + " " + r.URL.Path))
	})})
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/errands"}, {"GET", "/api/v1/runs"}, {"GET", "/api/v1/runs/1"},
		{"POST", "/api/v1/runs/1/answer"}, {"POST", "/api/v1/runs/1/stop"}, {"GET", "/api/v1/runs/1/log"},
		{"GET", "/api/v1/appointments"}, {"POST", "/api/v1/appointments/1/check"}, {"DELETE", "/api/v1/appointments/1"},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without the login: %d", c.method, c.path, rec.Code)
		}
		r.SetBasicAuth("helper", "helper")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK || rec.Body.String() != c.method+" "+c.path {
			t.Errorf("%s %s with the login: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}

func TestTheAIKeyIsBehindTheLogin(t *testing.T) {
	reg, _ := channels.NewRegistry(stubChannel("email"))
	h := New(Config{Login: testLogin, Channels: reg, AI: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Method + " " + r.URL.Path))
	})})
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/ai"}, {"PUT", "/api/v1/ai/key"}, {"DELETE", "/api/v1/ai/key"}, {"POST", "/api/v1/ai/check"},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without the login: %d", c.method, c.path, rec.Code)
		}
		r.SetBasicAuth("helper", "helper")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK || rec.Body.String() != c.method+" "+c.path {
			t.Errorf("%s %s with the login: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	// Only those four: nothing else under /api/v1/ai reaches the handler.
	r := httptest.NewRequest("GET", "/api/v1/ai/key", nil)
	r.SetBasicAuth("helper", "helper")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/ai/key: %d, want 404", rec.Code)
	}
}

func TestAccountsAndSignInsAreBehindTheLogin(t *testing.T) {
	reg, _ := channels.NewRegistry(stubChannel("email"))
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Method + " " + r.URL.Path))
	})
	h := New(Config{Login: testLogin, Channels: reg, SignIns: echo, Accounts: echo})
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/accounts"}, {"POST", "/api/v1/accounts"}, {"POST", "/api/v1/accounts/shop.test/register"},
		{"DELETE", "/api/v1/accounts/shop.test"}, {"PUT", "/api/v1/accounts/shop.test"}, // a card's edit
		{"POST", "/api/v1/accounts/suggest"}, {"GET", "/api/v1/signins/sites/shop.test/screen/vnc.html"},
		{"POST", "/api/v1/signins/sites/shop.test/live/done"},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without the login: %d", c.method, c.path, rec.Code)
		}
		r.SetBasicAuth("helper", "helper")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK || rec.Body.String() != c.method+" "+c.path {
			t.Errorf("%s %s with the login: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}
