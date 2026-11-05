package signins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// handlerFixture is a fixture behind the HTTP handler.
type handlerFixture struct {
	*fixture
	h http.Handler
}

func newHandlerFixture(t *testing.T) *handlerFixture {
	t.Helper()
	f := newFixture(t)
	return &handlerFixture{fixture: f, h: NewHandler(f.svc)}
}

// do runs one request and waits for the runs it started.
func (f *handlerFixture) do(method, path, body string) *httptest.ResponseRecorder {
	rec := f.serve(method, path, body)
	f.svc.Wait()
	return rec
}

// serve runs one request against the handler without waiting for the service's runs, which a
// waiting run would never finish.
func (f *handlerFixture) serve(method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// waitsForTheOwner starts an errand whose captcha ask holds until Done or Give up.
func (f *handlerFixture) waitsForTheOwner(t *testing.T) {
	t.Helper()
	h := f.hookWaits()
	f.captchaErrand(t, 1, make(chan answer, 1))
	opened(t, h)
}

func TestTheAccountsListHasEveryCardAndTheSettings(t *testing.T) {
	f := newHandlerFixture(t)
	f.signedUp(t, "registered")
	rec := f.do("GET", "/api/v1/accounts", "")
	var got struct {
		Settings Settings   `json:"settings"`
		Sites    []SiteView `json:"sites"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if got.Settings.Email != "owner@example.com" || !got.Settings.Screens || len(got.Sites) != 1 ||
		got.Sites[0].Key != shopKey || got.Sites[0].Account == nil || got.Sites[0].Account.State != "registered" {
		t.Fatalf("list = %s", rec.Body)
	}
}

func TestNoResponseCarriesThePassword(t *testing.T) {
	f := newHandlerFixture(t)
	f.signedUp(t, "registered")
	for _, path := range []string{"/api/v1/accounts"} {
		body := f.do("GET", path, "").Body.String()
		if strings.Contains(body, testPassword) || strings.Contains(strings.ToLower(body), "password") {
			t.Fatalf("%s: %s", path, body)
		}
	}
}

func TestRemoveAccountAndRemoveCardOverHTTP(t *testing.T) {
	f := newHandlerFixture(t)
	if rec := f.do("POST", "/api/v1/accounts/shop.test/remove", ""); rec.Code != http.StatusConflict {
		t.Fatalf("no account: %d %s", rec.Code, rec.Body)
	}
	f.signedUp(t, "registered")
	if rec := f.do("POST", "/api/v1/accounts/shop.test/remove", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("DELETE", "/api/v1/accounts/shop.test", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("a card without its account: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("DELETE", "/api/v1/accounts/shop.test", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("a gone card: %d %s", rec.Code, rec.Body)
	}
}

func TestIconIsServedAsAPNG(t *testing.T) {
	f := newHandlerFixture(t)
	if rec := f.do("GET", "/api/v1/accounts/shop.test/icon", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("no icon: %d", rec.Code)
	}
	if _, err := f.store.InsertCard(context.Background(), Card{Key: "mine.test", Name: "Mine", Link: "https://mine.test/", Icon: tinyPNG,
		AddedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	rec := f.do("GET", "/api/v1/accounts/mine.test/icon", "")
	png, _ := iconPNG(tinyPNG)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Body.String() != string(png) {
		t.Fatalf("icon: %d %q", rec.Code, rec.Header())
	}
}

func TestTheOldSignInRoutesAreGone(t *testing.T) {
	f := newHandlerFixture(t)
	for _, r := range [][2]string{{"GET", "/api/v1/signins"}, {"POST", "/api/v1/signins/sites"},
		{"POST", "/api/v1/signins/sites/shop.test/register"}, {"POST", "/api/v1/signins/sites/shop.test/check"},
		{"GET", "/api/v1/signins/sites/shop.test/paths"}, {"GET", "/api/v1/signins/sites/shop.test/runs/1"},
		{"GET", "/api/v1/signins/sites/shop.test/live"}} {
		if rec := f.serve(r[0], r[1], `{}`); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d", r[0], r[1], rec.Code)
		}
	}
}

func TestDoneAndGiveUp(t *testing.T) {
	for _, op := range []string{"done", "give-up"} {
		f := newHandlerFixture(t)
		path := "/api/v1/signins/sites/shop.test/live/" + op
		if rec := f.serve("POST", path, ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not waiting for you") {
			t.Fatalf("%s idle: %d %s", op, rec.Code, rec.Body)
		}
		f.waitsForTheOwner(t)
		if rec := f.serve("POST", path, ""); rec.Code != http.StatusNoContent {
			t.Fatalf("%s waiting: %d %s", op, rec.Code, rec.Body)
		}
		f.svc.Wait()
	}
}

func TestAnUnexpectedErrorIsAFixedBadGateway(t *testing.T) {
	f := newHandlerFixture(t)
	f.store.Close()
	rec := f.do("GET", "/api/v1/accounts", "")
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "database") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestASitesPageOverHTTP(t *testing.T) {
	f := newHandlerFixture(t)
	f.signedUp(t, "registered")
	rec := f.do("GET", "/api/v1/accounts/shop.test", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"site":{"key":"shop.test"`) || !strings.Contains(rec.Body.String(), `"runs":[]`) {
		t.Fatalf("site: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("GET", "/api/v1/accounts/nowhere.test", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("no card: %d %s", rec.Code, rec.Body)
	}
}

func TestCheckSignInOverHTTP(t *testing.T) {
	f := newHandlerFixture(t)
	if rec := f.do("POST", "/api/v1/accounts/shop.test/check", ""); rec.Code != http.StatusConflict {
		t.Fatalf("no account: %d %s", rec.Code, rec.Body)
	}
	f.signedUp(t, "registered")
	rec := f.do("POST", "/api/v1/accounts/shop.test/check", "")
	var got struct{ Run int64 }
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Run == 0 {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
}
