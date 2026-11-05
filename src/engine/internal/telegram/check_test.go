package telegram

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

var question = regexp.MustCompile(`^(\S+) 0 Concierge check: what is (\d+) \+ (\d+)\?$`)

func sentCheck(t *testing.T, f *fake) (to, answer string) {
	t.Helper()
	for _, s := range sentCalls(f) {
		if m := question.FindStringSubmatch(s); m != nil {
			a, _ := strconv.Atoi(m[2])
			b, _ := strconv.Atoi(m[3])
			return m[1], strconv.Itoa(a + b)
		}
	}
	t.Fatalf("no check sent: %v", sentCalls(f))
	return "", ""
}

func hit(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func checkState(h http.Handler) string {
	return strings.TrimSpace(hit(h, "GET", "/telegram/check", "").Body.String())
}

func TestTelegramCheckPassesInSavedMessages(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	h := NewHandler(linked(t, f, s))
	if rec := hit(h, "POST", "/telegram/check", `{}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	to, answer := sentCheck(t, f)
	if to != "self" {
		t.Fatalf("sent to %s, not Saved Messages", to)
	}
	if st := checkState(h); st != `{"state":"waiting"}` {
		t.Fatal(st)
	}
	reply := func(peer tg.PeerClass, id int) {
		m := msg(peer, id, answer)
		m.Out = true
		m.Date = int(time.Now().Add(time.Second).Unix())
		f.emit(NewMessage{Msg: m, Ent: ents})
	}
	reply(&tg.PeerUser{UserID: 1}, 90) // the answer in another chat does not count
	if st := checkState(h); st != `{"state":"waiting"}` {
		t.Fatal(st)
	}
	reply(&tg.PeerUser{UserID: ana.ID}, 91)
	eventually(t, "passed", func() bool { return checkState(h) == `{"state":"passed"}` })
}

func TestTelegramCheckNotConnected(t *testing.T) {
	s, _ := openTemp(t)
	rec := hit(NewHandler(newService(t, newFake(), s)), "POST", "/telegram/check", `{}`)
	if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"not connected"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
