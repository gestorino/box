package whatsapp

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func connectedService(t *testing.T) (*Service, *fakeClient, *Archive) {
	t.Helper()
	s, c, a := newService(t)
	c.account = me
	s.Start(ctx)
	c.emit(&events.Connected{})
	eventually(t, func() bool { return s.Status().State == "connected" })
	return s, c, a
}

func TestSendStoresTheMessageAsMine(t *testing.T) {
	s, c, a := connectedService(t)
	if err := s.Send(ctx, "+351 911 111 111", "hi"); err != nil {
		t.Fatal(err)
	}
	if c.count("SendText 351911111111@s.whatsapp.net hi") != 1 {
		t.Fatalf("%v", c.Calls())
	}
	msgs, _ := a.Messages(ctx, me, ana.String(), 10)
	if len(msgs) != 1 || msgs[0].ID != "S1" || !msgs[0].FromMe || msgs[0].Text != "hi" || msgs[0].Kind != "text" {
		t.Fatalf("%+v", msgs)
	}
	if err := s.Send(ctx, group.String(), "to the group"); err != nil { // a stored chat's JID
		t.Fatal(err)
	}
	if c.count("SendText "+group.String()+" to the group") != 1 {
		t.Fatalf("%v", c.Calls())
	}
}

func TestSendEchoIsStoredOnce(t *testing.T) {
	s, c, a := connectedService(t)
	s.Send(ctx, ana.String(), "hi")
	echo := evt(ana, types.NewJID("351910000000", types.DefaultUserServer), "S1", &waE2E.Message{Conversation: proto.String("hi")})
	echo.Info.IsFromMe = true
	c.emit(echo)
	if msgs, _ := a.Messages(ctx, me, ana.String(), 10); len(msgs) != 1 {
		t.Fatalf("%+v", msgs)
	}
}

func TestSendWhenNotConnectedIsRefused(t *testing.T) {
	for _, state := range []string{"unlinked", "pairing", "connecting", "logged_out"} {
		s, c, _ := newService(t)
		s.set(state)
		if err := s.Send(ctx, ana.String(), "hi"); !errors.Is(err, ErrNotConnected) {
			t.Fatalf("%s: %v", state, err)
		}
		for _, call := range c.Calls() {
			if strings.HasPrefix(call, "SendText") {
				t.Fatalf("%s: sent", state)
			}
		}
	}
}

func TestSendBadChat(t *testing.T) {
	s, _, _ := connectedService(t)
	for _, chat := range []string{"", "12", "ana", "+351abc", "status@broadcast"} {
		if err := s.Send(ctx, chat, "hi"); !errors.Is(err, errBadSend) {
			t.Fatalf("%q: %v", chat, err)
		}
	}
}

func TestSendRoute(t *testing.T) {
	s, c, _ := connectedService(t)
	h := NewHandler(s)
	if rec := call(h, "POST", "/whatsapp/send", `{"chat":"+351911111111","text":"hi"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"chat":"+351911111111","text":""}`, `{"chat":"+351911111111","text":"` + strings.Repeat("x", 4097) + `"}`,
		`{"chat":"nope","text":"hi"}`, `{"chat":"+351911111111","text":"hi","to":"x"}`} {
		if rec := call(h, "POST", "/whatsapp/send", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%.50s: %d", body, rec.Code)
		}
	}
	c.sendErr = errors.New("server returned error 479")
	if rec := call(h, "POST", "/whatsapp/send", `{"chat":"+351911111111","text":"hi"}`); rec.Code != http.StatusBadGateway ||
		strings.TrimSpace(rec.Body.String()) != `{"error":"message not sent"}` {
		t.Fatalf("send error: %d %s", rec.Code, rec.Body)
	}
	s.set("logged_out")
	if rec := call(h, "POST", "/whatsapp/send", `{"chat":"+351911111111","text":"hi"}`); rec.Code != http.StatusConflict ||
		strings.TrimSpace(rec.Body.String()) != `{"error":"not connected"}` {
		t.Fatalf("not connected: %d %s", rec.Code, rec.Body)
	}
}
