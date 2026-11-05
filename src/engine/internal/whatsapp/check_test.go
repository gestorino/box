package whatsapp

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

var question = regexp.MustCompile(`^SendText (\S+) Concierge check: what is (\d+) \+ (\d+)\?$`)

// sentCheck is where the check went and its answer.
func sentCheck(t *testing.T, c *fakeClient) (to, answer string) {
	t.Helper()
	for _, call := range c.Calls() {
		if m := question.FindStringSubmatch(call); m != nil {
			a, _ := strconv.Atoi(m[2])
			b, _ := strconv.Atoi(m[3])
			return m[1], strconv.Itoa(a + b)
		}
	}
	t.Fatalf("no check sent: %v", c.Calls())
	return "", ""
}

func hit(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func checkState(h http.Handler) string {
	return strings.TrimSpace(hit(h, "GET", "/whatsapp/check", "").Body.String())
}

// replyFromPhone is the owner typing text in their own chat on the phone: it reaches the box as
// the owner's own message, in chat.
func replyFromPhone(c *fakeClient, chat types.JID, id, text string) {
	e := evt(chat, types.NewJID("351910000000", types.DefaultUserServer), id, &waE2E.Message{Conversation: proto.String(text)})
	e.Info.IsFromMe = true
	e.Info.Timestamp = time.Now().Add(time.Second)
	c.emit(e)
}

func TestWhatsAppCheckPassesOnASelfChatReply(t *testing.T) {
	s, c, _ := connectedService(t)
	h := NewHandler(s)
	if rec := hit(h, "POST", "/whatsapp/check", `{}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	to, answer := sentCheck(t, c)
	if to != me {
		t.Fatalf("sent to %s, not the account's own chat", to)
	}
	if s := checkState(h); s != `{"state":"waiting"}` {
		t.Fatal(s)
	}
	replyFromPhone(c, ana, "R0", answer) // the answer in another chat does not count
	if s := checkState(h); s != `{"state":"waiting"}` {
		t.Fatal(s)
	}
	replyFromPhone(c, types.NewJID("351910000000", types.DefaultUserServer), "R1", answer)
	if s := checkState(h); s != `{"state":"passed"}` {
		t.Fatal(s)
	}
}

// Typed on the phone, a self-chat message may name the chat by the owner's LID.
func TestWhatsAppCheckReplyUnderTheOwnersLID(t *testing.T) {
	s, c, _ := connectedService(t)
	lid := types.NewJID("99999999999999", types.HiddenUserServer)
	c.lids[lid] = types.NewJID("351910000000", types.DefaultUserServer)
	h := NewHandler(s)
	hit(h, "POST", "/whatsapp/check", `{}`)
	_, answer := sentCheck(t, c)
	replyFromPhone(c, lid, "R1", answer)
	if s := checkState(h); s != `{"state":"passed"}` {
		t.Fatal(s)
	}
}

func TestWhatsAppCheckNotConnected(t *testing.T) {
	s, _, _ := newService(t)
	s.set("unlinked")
	rec := hit(NewHandler(s), "POST", "/whatsapp/check", `{}`)
	if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"not connected"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
