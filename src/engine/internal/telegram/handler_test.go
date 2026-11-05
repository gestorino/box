package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestHandlerCredentials(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	h := NewHandler(newService(t, f, s))

	if rec := call(h, "GET", "/telegram/status", ""); strings.TrimSpace(rec.Body.String()) != `{"state":"no_credentials","credentials":false,"archive_channels":false}` {
		t.Fatalf("status %s", rec.Body)
	}
	for _, body := range []string{`{"api_id":1}`, `{"api_id":1,"api_hash":"x","extra":1}`, `{"api_hash":"x"}`, `nope`} {
		if rec := call(h, "POST", "/telegram/credentials", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	f.with(func(f *fake) { f.checkErr = ErrCredentials })
	rec := call(h, "POST", "/telegram/credentials", `{"api_id":12345,"api_hash":"0123456789abcdef0123456789abcdef"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Telegram refused the api_id or api_hash") ||
		strings.Contains(rec.Body.String(), "0123456789abcdef") {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	f.with(func(f *fake) { f.checkErr = nil })
	if rec := call(h, "POST", "/telegram/credentials", `{"api_id":12345,"api_hash":"0123456789abcdef0123456789abcdef"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("accepted: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "GET", "/telegram/status", ""); !strings.Contains(rec.Body.String(), `"api_id":12345`) || strings.Contains(rec.Body.String(), "abcdef") {
		t.Fatalf("status %s", rec.Body)
	}
}

func TestHandlerLinkQRPassword(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	f.qrURLs, f.qrResult, f.password, f.qrBlock = []string{"tg://login?token=AAA"}, ErrPasswordNeeded, "s3cret", make(chan struct{})
	svc := newService(t, f, s)
	svc.SetCredentials(ctx, creds)
	h := NewHandler(svc)

	if rec := call(h, "GET", "/telegram/qr", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("qr before pairing: %d", rec.Code)
	}
	if rec := call(h, "POST", "/telegram/password", `{"password":"x"}`); rec.Code != http.StatusConflict {
		t.Fatalf("password while unlinked: %d", rec.Code)
	}
	if rec := call(h, "POST", "/telegram/link", "{}"); rec.Code != http.StatusAccepted {
		t.Fatalf("link: %d", rec.Code)
	}
	if rec := call(h, "POST", "/telegram/link", "{}"); rec.Code != http.StatusConflict {
		t.Fatalf("second link: %d", rec.Code)
	}
	eventually(t, "qr", func() bool { _, _, ok := svc.QR(); return ok })
	var qr struct {
		URL       string `json:"url"`
		ExpiresIn int    `json:"expires_in"`
	}
	rec := call(h, "GET", "/telegram/qr", "")
	json.Unmarshal(rec.Body.Bytes(), &qr)
	if rec.Code != 200 || qr.URL != "tg://login?token=AAA" || qr.ExpiresIn < 28 || qr.ExpiresIn > 30 {
		t.Fatalf("qr %d %s", rec.Code, rec.Body)
	}
	close(f.qrBlock)
	waitState(t, svc, "password_needed")
	if rec := call(h, "POST", "/telegram/password", `{"password":"wrong"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Wrong password") {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/telegram/password", `{"password":"s3cret"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("password: %d", rec.Code)
	}
}

func TestHandlerChatsSettingsUnlink(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	s.Save(ctx, Row{Account: me, Peer: "user:1", ID: 1, Sender: "user:1", SenderName: "Ana", SentAt: t0, Kind: "text", Text: "hi"})
	h := NewHandler(svc)

	if rec := call(h, "GET", "/telegram/chats", ""); !strings.Contains(rec.Body.String(), `"last_text":"hi"`) {
		t.Fatalf("chats %s", rec.Body)
	}
	if rec := call(h, "GET", "/telegram/messages", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("messages without chat: %d", rec.Code)
	}
	if rec := call(h, "GET", "/telegram/messages?chat=user:1", ""); !strings.Contains(rec.Body.String(), `"sender_name":"Ana"`) {
		t.Fatalf("messages %s", rec.Body)
	}
	if rec := call(h, "POST", "/telegram/settings", `{"archive_channels":true}`); rec.Code != http.StatusNoContent || !svc.Status().ArchiveChannels {
		t.Fatalf("settings: %d", rec.Code)
	}
	if rec := call(h, "POST", "/telegram/settings", `{"archive":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown setting: %d", rec.Code)
	}
	if rec := call(h, "POST", "/telegram/unlink", `{"delete":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := call(h, "POST", "/telegram/unlink", `{"delete_chats":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("unlink: %d", rec.Code)
	}
	if rec := call(h, "POST", "/telegram/unlink", `{"delete_chats":true}`); rec.Code != http.StatusConflict {
		t.Fatalf("second unlink: %d", rec.Code)
	}
}

func TestRemoveRoute(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	svc.logoutTimeout = 50 * time.Millisecond
	f.with(func(f *fake) { f.logoutHang = true })
	h := NewHandler(svc)
	if rec := call(h, "POST", "/telegram/remove", `{"delete_data":false,"force":false}`); rec.Code != http.StatusFailedDependency ||
		strings.TrimSpace(rec.Body.String()) != `{"error":"couldn't reach Telegram to log out"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/telegram/remove", `{"delete_data":false,"force":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/telegram/remove", `{"force":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
}
