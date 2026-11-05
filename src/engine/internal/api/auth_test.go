package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"concierge/engine/internal/channels"
)

// testLogin is helper/helper, hashed once for the package's tests.
var testLogin = func() Login {
	hash, err := bcrypt.GenerateFromPassword([]byte("helper"), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	return Login{User: "helper", PasswordHash: string(hash)}
}()

func login(*testing.T) Login { return testLogin }

func authed(h http.Handler, path, user, pass string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" || pass != "" {
		r.SetBasicAuth(user, pass)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestLoginRequiredEverywhereButHealthz(t *testing.T) {
	h := requireLogin(login(t), ok)
	for _, path := range []string{"/", "/sms", "/api/v1/status", "/api/v1/network/wifi", "/assets/app.js"} {
		rec := authed(h, path, "", "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without login: got %d, want 401", path, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="Concierge", charset="UTF-8"` {
			t.Errorf("%s: WWW-Authenticate %q", path, got)
		}
	}
	if rec := authed(h, "/healthz", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("/healthz must stay open: got %d", rec.Code)
	}
}

func TestLoginAcceptsOnlyTheRightPair(t *testing.T) {
	h := requireLogin(login(t), ok)
	for _, c := range []struct {
		user, pass string
		want       int
	}{
		{"helper", "helper", http.StatusOK},
		{"helper", "wrong", http.StatusUnauthorized},
		{"admin", "helper", http.StatusUnauthorized},
		{"helper", "", http.StatusUnauthorized},
	} {
		// Twice: the second time exercises the remembered-login path.
		for range 2 {
			if rec := authed(h, "/api/v1/status", c.user, c.pass); rec.Code != c.want {
				t.Errorf("%s/%s: got %d, want %d", c.user, c.pass, rec.Code, c.want)
			}
		}
	}
}

func TestNewRefusesAnIncompleteLogin(t *testing.T) {
	for _, l := range []Login{{}, {User: "helper"}, {PasswordHash: "x"}} {
		if err := l.Validate(); err == nil {
			t.Errorf("%+v: want an error", l)
		}
	}
	if err := login(t).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNewWithoutALoginLetsNothingThrough(t *testing.T) {
	reg, _ := channels.NewRegistry()
	if rec := authed(New(Config{Channels: reg}), "/api/v1/status", "helper", "helper"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}
