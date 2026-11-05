package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func guarded(method, path string, headers map[string]string) int {
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	r.Host = "192.0.2.10"
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	guardWrites(ok).ServeHTTP(rec, r)
	return rec.Code
}

func TestGuardAllowsReads(t *testing.T) {
	if got := guarded(http.MethodGet, "/api/v1/status", map[string]string{"Origin": "http://evil.example"}); got != http.StatusOK {
		t.Fatalf("got %d", got)
	}
}

func TestGuardRequiresJSON(t *testing.T) {
	got := guarded(http.MethodPost, "/api/v1/network/wifi/scan",
		map[string]string{"Content-Type": "text/plain", "Sec-Fetch-Site": "same-origin"})
	if got != http.StatusForbidden {
		t.Fatalf("got %d, want 403", got)
	}
}

func TestGuardRefusesCrossSite(t *testing.T) {
	for _, h := range []map[string]string{
		{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"},
		{"Content-Type": "application/json", "Origin": "http://evil.example"},
	} {
		if got := guarded(http.MethodPost, "/api/v1/network/wifi/scan", h); got != http.StatusForbidden {
			t.Errorf("%v: got %d, want 403", h, got)
		}
	}
}

func TestGuardAllowsSameOrigin(t *testing.T) {
	for _, h := range []map[string]string{
		{"Content-Type": "application/json; charset=utf-8", "Sec-Fetch-Site": "same-origin"},
		{"Content-Type": "application/json", "Origin": "http://192.0.2.10"},
	} {
		if got := guarded(http.MethodPut, "/api/v1/network/cellular", h); got != http.StatusOK {
			t.Errorf("%v: got %d, want 200", h, got)
		}
	}
}

// curl and other non-browser clients send neither header; they cannot be a cross-site request.
func TestGuardAllowsNonBrowserJSON(t *testing.T) {
	if got := guarded(http.MethodDelete, "/api/v1/network/wifi/saved/x", map[string]string{"Content-Type": "application/json"}); got != http.StatusOK {
		t.Fatalf("got %d", got)
	}
}

func TestGuardLeavesNonAPIAlone(t *testing.T) {
	if got := guarded(http.MethodPost, "/", map[string]string{"Content-Type": "text/plain"}); got != http.StatusOK {
		t.Fatalf("got %d", got)
	}
}
