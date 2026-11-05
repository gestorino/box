package email

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"concierge/engine/internal/secrets/secretstest"
)

func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

const connectBody = `{"address":" Owner@Example.com ","app_password":"abcd efgh ijkl mnop","history":"copy","copy_on_mobile":false}`

// connected is a handler over a service connected to the fake server, with raws stored.
func connected(t *testing.T, raws ...[]byte) (http.Handler, *srig) {
	t.Helper()
	g := newSrig(t)
	h := NewHandler(g.s, g.a, g.ls())
	if rec := call(h, "POST", "/email/connect", connectBody); rec.Code != http.StatusAccepted {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
	for i, raw := range raws {
		if err := g.a.Save(ctx, me, uint32(i+1), t0, raw); err != nil {
			t.Fatal(err)
		}
	}
	return h, g
}

func firstEmailID(t *testing.T, g *srig, thread string) int64 {
	t.Helper()
	emails, err := g.a.Thread(ctx, me, thread)
	if err != nil || len(emails) == 0 {
		t.Fatalf("%v %v", emails, err)
	}
	return emails[0].ID
}

func TestConnectNormalisesInput(t *testing.T) {
	_, g := connected(t) // the fake server accepts only the lower-cased address and the unspaced password
	if st := g.s.Status(); st.State != "connecting" || st.Address != me {
		t.Fatalf("%+v", st)
	}
}

func TestConnectRefused422(t *testing.T) {
	g := newSrig(t)
	h := NewHandler(g.s, g.a, g.ls())
	rec := call(h, "POST", "/email/connect", `{"address":"`+me+`","app_password":"wrong","history":"copy","copy_on_mobile":false}`)
	if rec.Code != http.StatusUnprocessableEntity || strings.TrimSpace(rec.Body.String()) != `{"error":"Google refused the address or app password"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestConnectValidation(t *testing.T) {
	g := newSrig(t)
	h := NewHandler(g.s, g.a, g.ls())
	for _, body := range []string{
		`{"address":"` + me + `","app_password":"p","history":"all","copy_on_mobile":false}`,
		`{"address":"","app_password":"p","history":"copy","copy_on_mobile":false}`,
		`{"address":"` + me + `","app_password":"  ","history":"copy","copy_on_mobile":false}`,
		`{"address":"` + me + `","app_password":"p","history":"copy","copy_on_mobile":false,"extra":1}`,
		`not json`,
	} {
		if rec := call(h, "POST", "/email/connect", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	call(h, "POST", "/email/connect", connectBody)
	if rec := call(h, "POST", "/email/connect", connectBody); rec.Code != http.StatusConflict {
		t.Fatalf("connect while connected: %d", rec.Code)
	}
}

func TestPasswordNeverInResponses(t *testing.T) {
	h, _ := connected(t, fixture(t, "plain.eml"))
	for _, rec := range []*httptest.ResponseRecorder{
		call(h, "GET", "/email/status", ""),
		call(h, "GET", "/email/threads", ""),
		call(h, "GET", "/email/thread?id=root@example.com", ""),
		call(h, "POST", "/email/connect", connectBody),
		call(h, "POST", "/email/connect", `{"app_password":"abcd efgh ijkl mnop"`),
	} {
		if body := rec.Body.String(); strings.Contains(body, "abcdefghijklmnop") || strings.Contains(body, "abcd efgh") {
			t.Fatalf("password in %s", body)
		}
	}
}

func TestReadsServeTheAccount(t *testing.T) {
	h, _ := connected(t, fixture(t, "plain.eml"), fixture(t, "reply.eml"))
	var threads []Thread
	json.Unmarshal(call(h, "GET", "/email/threads", "").Body.Bytes(), &threads)
	if len(threads) != 1 || threads[0].Count != 2 {
		t.Fatalf("%+v", threads)
	}
	var emails []Email
	json.Unmarshal(call(h, "GET", "/email/thread?id=root@example.com", "").Body.Bytes(), &emails)
	if len(emails) != 2 || emails[1].Direction != "out" {
		t.Fatalf("%+v", emails)
	}
}

func TestThreadNeedsID(t *testing.T) {
	h, _ := connected(t)
	if rec := call(h, "GET", "/email/thread", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
}

func TestHTMLHeaders(t *testing.T) {
	h, g := connected(t, fixture(t, "hostile.eml"), fixture(t, "plain.eml"))
	rec := call(h, "GET", fmt.Sprintf("/email/emails/%d/html", firstEmailID(t, g, "hostile@evil.example")), "")
	if rec.Code != http.StatusOK ||
		rec.Header().Get("Content-Security-Policy") != "default-src 'none'; style-src 'unsafe-inline'; img-src data: http: https:; sandbox allow-popups allow-popups-to-escape-sandbox" ||
		rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(rec.Body.String(), `target="_blank"`) {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if rec := call(h, "GET", fmt.Sprintf("/email/emails/%d/html", firstEmailID(t, g, "root@example.com")), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("text-only email: %d", rec.Code)
	}
	for _, path := range []string{"/email/emails/999/html", "/email/emails/x/html"} {
		if rec := call(h, "GET", path, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
}

func TestAttachmentDownloadHeaders(t *testing.T) {
	h, g := connected(t, fixture(t, "attachments.eml"))
	id := firstEmailID(t, g, "files@example.com")
	get := func(idx int) *httptest.ResponseRecorder {
		return call(h, "GET", fmt.Sprintf("/email/emails/%d/attachments/%d", id, idx), "")
	}
	for idx, want := range map[int]struct{ disposition, typ, body string }{
		0: {"attachment; filename=attachment", "application/octet-stream", "\x01\x02\x03\x04"},
		1: {"attachment; filename*=utf-8''Contrato%20%C3%A9.pdf", "application/pdf", "%PDF-1.4\n"},
		3: {"attachment; filename=passwd", "text/plain", "root"},
	} {
		rec := get(idx)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Disposition") != want.disposition ||
			rec.Header().Get("Content-Type") != want.typ || rec.Header().Get("X-Content-Type-Options") != "nosniff" ||
			rec.Header().Get("Content-Security-Policy") != "sandbox" || strings.TrimRight(rec.Body.String(), "\r\n") != strings.TrimRight(want.body, "\r\n") {
			t.Fatalf("attachment %d: %d %v %q", idx, rec.Code, rec.Header(), rec.Body)
		}
	}
	if rec := get(9); rec.Code != http.StatusNotFound {
		t.Fatalf("no attachment 9: %d", rec.Code)
	}
}

func TestDisconnectKeepOrDelete(t *testing.T) {
	h, g := connected(t, fixture(t, "plain.eml"))
	if rec := call(h, "POST", "/email/disconnect", `{"delete_emails":false}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/email/disconnect", `{"delete_emails":false}`); rec.Code != http.StatusConflict {
		t.Fatalf("again: %d", rec.Code)
	}
	var threads []Thread
	json.Unmarshal(call(h, "GET", "/email/threads", "").Body.Bytes(), &threads)
	if len(threads) != 1 || g.s.Status() != (Status{State: "disconnected", Address: me}) {
		t.Fatalf("kept emails must stay readable: %+v %+v", threads, g.s.Status())
	}
	call(h, "POST", "/email/connect", connectBody)
	if rec := call(h, "POST", "/email/disconnect", `{"delete_emails":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d", rec.Code)
	}
	if body := strings.TrimSpace(call(h, "GET", "/email/threads", "").Body.String()); body != "[]" {
		t.Fatalf("%s", body)
	}
	if rec := call(h, "POST", "/email/disconnect", `{"delete_emails":"yes"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
}

func TestRemoveRoute(t *testing.T) {
	h, g := connected(t, fixture(t, "plain.eml"))
	if rec := call(h, "POST", "/email/remove", `{"delete_data":true,"force":false}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/email/remove", `{"delete_data":true,"force":false}`); rec.Code != http.StatusNoContent {
		t.Fatalf("again: %d", rec.Code)
	}
	if g.count(t) != 0 {
		t.Fatal("not wiped")
	}
	if rec := call(h, "POST", "/email/remove", `{"delete_data":"yes"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
}

func TestSendAndReplyRoutes(t *testing.T) {
	h, g := connected(t, rawMail("Ana Silva <ana@example.com>", "root@x", "", "Lease", "hi"))
	if rec := call(h, "POST", "/email/send", `{"to":" Ana@Example.com ","subject":"Hi","text":"hello"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("send: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/email/reply", `{"thread":"root@x","text":"fine"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("reply: %d %s", rec.Code, rec.Body)
	}
	want := []Outgoing{{To: "ana@example.com", Subject: "Hi", Text: "hello"}, {To: "ana@example.com", Subject: "Re: Lease", Text: "fine", InReplyTo: "root@x"}}
	for i := range g.sent { // each send gets its own Message-ID
		if g.sent[i].m.MessageID == "" {
			t.Fatalf("no Message-ID: %+v", g.sent[i])
		}
		g.sent[i].m.MessageID = ""
	}
	if len(g.sent) != 2 || g.sent[0].m != want[0] || g.sent[1].m != want[1] {
		t.Fatalf("%+v", g.sent)
	}
	if rec := call(h, "POST", "/email/reply", `{"thread":"nope@x","text":"fine"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown thread: %d", rec.Code)
	}
	for _, body := range []string{`{"to":"nobody","subject":"s","text":"x"}`, `{"to":"a@b.c","subject":"s","text":""}`,
		`{"to":"a@b.c","subject":"s","text":"` + strings.Repeat("x", 100001) + `"}`, `{"to":"a@b.c","subject":"s","text":"x","cc":"z"}`} {
		if rec := call(h, "POST", "/email/send", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%.50s: %d", body, rec.Code)
		}
	}
	g.sendErr = errors.New("smtp: 550 5.1.1 ana@example.com does not exist")
	if rec := call(h, "POST", "/email/send", `{"to":"a@b.c","subject":"s","text":"x"}`); rec.Code != http.StatusBadGateway ||
		strings.TrimSpace(rec.Body.String()) != `{"error":"email not sent"}` {
		t.Fatalf("send error: %d %s", rec.Code, rec.Body)
	}
	g.s.Disconnect(ctx, false)
	if rec := call(h, "POST", "/email/send", `{"to":"a@b.c","subject":"s","text":"x"}`); rec.Code != http.StatusConflict {
		t.Fatalf("no account: %d", rec.Code)
	}
}

func TestSecretdDownAnswersPlainly(t *testing.T) {
	g := newSrig(t)
	h := NewHandler(g.s, g.a, g.ls())
	call(h, "POST", "/email/connect", connectBody)
	fresh := NewHandler(g.serviceWith(secretstest.Down(t)), g.a, g.ls()) // follows nothing
	s := g.serviceWith(secretstest.Down(t))
	if err := s.resume(ctx); err != nil {
		t.Fatal(err)
	}
	h = NewHandler(s, g.a, g.ls()) // follows the account, as after a restart
	const want = `{"error":"secrets unavailable"}`
	for _, c := range []struct {
		h          http.Handler
		path, body string
		code       int
	}{
		{fresh, "/email/connect", connectBody, http.StatusBadGateway},
		{h, "/email/remove", `{"delete_data":false,"force":false}`, http.StatusBadGateway},
		{h, "/email/remove", `{"delete_data":false,"force":true}`, http.StatusBadGateway}, // never forced past the secret
		{h, "/email/disconnect", `{"delete_emails":false}`, http.StatusBadGateway},
	} {
		rec := call(c.h, "POST", c.path, c.body)
		if rec.Code != c.code || strings.TrimSpace(rec.Body.String()) != want {
			t.Fatalf("%s: %d %s", c.path, rec.Code, rec.Body)
		}
	}
}

func TestSendWithSecretdDown502(t *testing.T) {
	_, g := connected(t)
	s := g.serviceWith(secretstest.Down(t))
	if err := s.resume(ctx); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, g.a, g.ls())
	rec := call(h, "POST", "/email/send", `{"to":"ana@example.com","subject":"Hi","text":"hello"}`)
	if rec.Code != http.StatusBadGateway || strings.TrimSpace(rec.Body.String()) != `{"error":"secrets unavailable"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
