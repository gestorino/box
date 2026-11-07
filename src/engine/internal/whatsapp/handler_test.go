package whatsapp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestHandlerLinkQRAndStatus(t *testing.T) {
	s, c, _ := newService(t)
	s.Start(ctx)
	h := NewHandler(s, NewListen(s.archive, nil))

	if rec := call(h, "GET", "/whatsapp/status", ""); strings.TrimSpace(rec.Body.String()) != `{"state":"unlinked"}` {
		t.Fatalf("status %s", rec.Body)
	}
	if rec := call(h, "GET", "/whatsapp/qr", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("qr before pairing: %d", rec.Code)
	}
	if rec := call(h, "POST", "/whatsapp/link", "{}"); rec.Code != http.StatusAccepted {
		t.Fatalf("link: %d", rec.Code)
	}
	if rec := call(h, "POST", "/whatsapp/link", "{}"); rec.Code != http.StatusConflict {
		t.Fatalf("second link: %d", rec.Code)
	}
	c.qr <- QRItem{Event: "code", Code: "2@abc", Timeout: 20 * time.Second}
	eventually(t, func() bool { _, _, ok := s.QR(); return ok })
	var qr struct {
		Code      string `json:"code"`
		ExpiresIn int    `json:"expires_in"`
	}
	rec := call(h, "GET", "/whatsapp/qr", "")
	json.Unmarshal(rec.Body.Bytes(), &qr)
	if rec.Code != 200 || qr.Code != "2@abc" || qr.ExpiresIn != 20 {
		t.Fatalf("qr %d %s", rec.Code, rec.Body)
	}
}

func TestHandlerUnlinkReadsAndValidation(t *testing.T) {
	s, c, a := newService(t)
	c.account = me
	s.Start(ctx)
	c.emit(&events.Connected{})
	a.Save(ctx, row(ana.String(), "1", "hi", t0))
	h := NewHandler(s, NewListen(s.archive, nil))

	if rec := call(h, "GET", "/whatsapp/chats", ""); !strings.Contains(rec.Body.String(), `"last_text":"hi"`) {
		t.Fatalf("chats %s", rec.Body)
	}
	if rec := call(h, "GET", "/whatsapp/messages?chat="+ana.String(), ""); !strings.Contains(rec.Body.String(), `"text":"hi"`) {
		t.Fatalf("messages %s", rec.Body)
	}
	if rec := call(h, "GET", "/whatsapp/messages", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("messages without chat: %d", rec.Code)
	}
	if rec := call(h, "POST", "/whatsapp/link", "{}"); rec.Code != http.StatusConflict {
		t.Fatalf("link while connected: %d", rec.Code)
	}
	if rec := call(h, "POST", "/whatsapp/unlink", `{"delete_chats":true,"extra":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := call(h, "POST", "/whatsapp/unlink", `{"delete_chats":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("unlink: %d %s", rec.Code, rec.Body)
	}
	if chats, _ := a.Chats(ctx, me); len(chats) != 0 {
		t.Fatalf("chats not deleted: %+v", chats)
	}
	if rec := call(h, "POST", "/whatsapp/unlink", `{"delete_chats":false}`); rec.Code != http.StatusConflict {
		t.Fatalf("unlink while unlinked: %d", rec.Code)
	}
}

func TestRemoveRoute(t *testing.T) {
	s, c, _ := newService(t)
	c.account = me
	c.logoutErr = errors.New("offline")
	s.Start(ctx)
	h := NewHandler(s, NewListen(s.archive, nil))
	if rec := call(h, "POST", "/whatsapp/remove", `{"delete_data":false,"force":false}`); rec.Code != http.StatusFailedDependency ||
		strings.TrimSpace(rec.Body.String()) != `{"error":"couldn't reach WhatsApp to log out"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/whatsapp/remove", `{"delete_data":false,"force":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/whatsapp/remove", `{"delete_data":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
}
