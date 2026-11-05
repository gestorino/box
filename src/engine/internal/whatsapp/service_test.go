package whatsapp

import (
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func newService(t *testing.T) (*Service, *fakeClient, *Archive) {
	t.Helper()
	a, _ := openTemp(t)
	c := newFakeClient()
	return NewService(c, a, func() time.Time { return t0 }), c, a
}

// eventually waits for a condition the service reaches in its own goroutine.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never met")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStartReconnectsALinkedSession(t *testing.T) {
	s, c, _ := newService(t)
	c.account = me
	c.pushName = "Box"
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return c.count("Connect") == 1 }) // connects in the background
	if c.count("QRChannel") != 0 {
		t.Fatalf("calls %v", c.Calls())
	}
	if st := s.Status(); st.State != "connecting" {
		t.Fatalf("status %+v", st)
	}
	c.emit(&events.Connected{})
	if st := s.Status(); st != (Status{State: "connected", Phone: "+351910000000", Name: "Box"}) {
		t.Fatalf("status %+v", st)
	}
}

func TestStartUnlinkedOrLoggedOut(t *testing.T) {
	s, _, _ := newService(t)
	s.Start(ctx)
	if s.Status().State != "unlinked" {
		t.Fatalf("%+v", s.Status())
	}
	s2, _, a := newService(t)
	a.SetState(ctx, "logged_out")
	s2.Start(ctx)
	if s2.Status().State != "logged_out" {
		t.Fatalf("%+v", s2.Status())
	}
}

func TestLinkFromUnlinkedAndLoggedOut(t *testing.T) {
	s, c, a := newService(t)
	a.SetState(ctx, "logged_out")
	s.Start(ctx)
	if err := s.Link(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Status().State != "pairing" || c.count("QRChannel") != 1 || c.count("Connect") != 1 {
		t.Fatalf("%+v %v", s.Status(), c.Calls())
	}
	if err := s.Link(ctx); !errors.Is(err, ErrState) {
		t.Fatalf("second link: %v", err)
	}
}

func TestQRRotatesAndTimesOut(t *testing.T) {
	s, c, _ := newService(t)
	s.Start(ctx)
	s.Link(ctx)
	c.qr <- QRItem{Event: "code", Code: "2@first", Timeout: 60 * time.Second}
	c.qr <- QRItem{Event: "code", Code: "2@second", Timeout: 20 * time.Second}
	eventually(t, func() bool { code, _, _ := s.QR(); return code == "2@second" })
	if _, exp, ok := s.QR(); !ok || exp != 20*time.Second {
		t.Fatalf("expires %v ok=%t", exp, ok)
	}
	c.qr <- QRItem{Event: "timeout"}
	eventually(t, func() bool { return s.Status().State == "unlinked" })
	if _, _, ok := s.QR(); ok {
		t.Fatal("a QR code after the pairing ended")
	}
}

func TestPairSuccessConnectsAndClearsLoggedOut(t *testing.T) {
	s, c, a := newService(t)
	a.SetState(ctx, "logged_out")
	s.Start(ctx)
	s.Link(ctx)
	c.account = me
	c.emit(&events.PairSuccess{})
	c.qr <- QRItem{Event: "success"}
	eventually(t, func() bool { return s.Status().State == "connecting" })
	if st, _ := a.State(ctx); st != "" {
		t.Fatalf("logged_out flag left: %q", st)
	}
	c.emit(&events.Connected{})
	if s.Status().State != "connected" {
		t.Fatalf("%+v", s.Status())
	}
}

func TestLoggedOutEventPersists(t *testing.T) {
	s, c, a := newService(t)
	c.account = me
	s.Start(ctx)
	c.emit(&events.LoggedOut{})
	if s.Status().State != "logged_out" || c.count("Forget") != 1 {
		t.Fatalf("%+v %v", s.Status(), c.Calls())
	}
	again := NewService(newFakeClient(), a, func() time.Time { return t0 })
	again.Start(ctx)
	if again.Status().State != "logged_out" {
		t.Fatalf("after restart: %+v", again.Status())
	}
}

func TestUnlinkKeepsOrDeletesChats(t *testing.T) {
	for _, del := range []bool{false, true} {
		s, c, a := newService(t)
		c.account = me
		s.Start(ctx)
		a.Save(ctx, row("ana@s.whatsapp.net", "1", "hi", t0))
		if err := s.Unlink(ctx, del); err != nil {
			t.Fatal(err)
		}
		if s.Status().State != "unlinked" || c.count("Logout") != 1 {
			t.Fatalf("%+v %v", s.Status(), c.Calls())
		}
		chats, _ := a.Chats(ctx, me)
		if del != (len(chats) == 0) {
			t.Fatalf("delete=%t, chats %+v", del, chats)
		}
	}
	s, _, _ := newService(t)
	s.Start(ctx)
	if err := s.Unlink(ctx, false); !errors.Is(err, ErrState) {
		t.Fatalf("unlink while unlinked: %v", err)
	}
}

func TestMessagesArchivedFromEvents(t *testing.T) {
	s, c, _ := newService(t)
	c.account = me
	c.groups[group.String()] = "Family"
	s.Start(ctx)
	c.emit(evt(ana, ana, "1", &waE2E.Message{Conversation: proto.String("hi")}))
	c.emit(evt(group, ana, "2", &waE2E.Message{Conversation: proto.String("hello all")}))
	c.emit(evt(group, ana, "3", &waE2E.Message{Conversation: proto.String("again")}))
	c.emit(evt(ana, ana, "4", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("1")},
	}}))
	chats, _ := s.Chats(ctx)
	if len(chats) != 2 {
		t.Fatalf("chats %+v", chats)
	}
	byJID := map[string]Chat{chats[0].JID: chats[0], chats[1].JID: chats[1]}
	if byJID[group.String()].Name != "Family" || !byJID[group.String()].IsGroup || byJID[ana.String()].Name != "Ana" {
		t.Fatalf("names %+v", chats)
	}
	if c.count("GroupName "+group.String()) != 1 {
		t.Fatalf("group name looked up %d times", c.count("GroupName "+group.String()))
	}
	msgs, _ := s.Messages(ctx, ana.String())
	if len(msgs) != 1 || msgs[0].Text != "This message was deleted" {
		t.Fatalf("msgs %+v", msgs)
	}
}

func TestConnectedSetsPresenceUnavailable(t *testing.T) {
	s, c, _ := newService(t)
	c.account = me
	s.Start(ctx)
	c.emit(&events.Connected{})
	eventually(t, func() bool { return c.count("PresenceUnavailable") == 1 })
}

// whatsmeow ends an unscanned QR by disconnecting first, then sends "timeout" on the QR channel; the
// disconnect must not leave the service believing it is reconnecting a linked session.
func TestDisconnectDuringPairingReturnsToUnlinked(t *testing.T) {
	s, c, _ := newService(t)
	s.Start(ctx)
	s.Link(ctx)
	c.emit(&events.Disconnected{})
	c.qr <- QRItem{Event: "timeout"}
	eventually(t, func() bool { return s.Status().State == "unlinked" })
	if err := s.Link(ctx); err != nil {
		t.Fatalf("linking again: %v", err)
	}
	if s.Status().State != "pairing" {
		t.Fatalf("%+v", s.Status())
	}
}

func TestPasskeyItemsDoNotEndPairing(t *testing.T) {
	s, c, _ := newService(t)
	s.Start(ctx)
	s.Link(ctx)
	c.qr <- QRItem{Event: "passkey-request"}
	c.qr <- QRItem{Event: "code", Code: "2@x", Timeout: time.Minute}
	eventually(t, func() bool { _, _, ok := s.QR(); return ok })
	if s.Status().State != "pairing" {
		t.Fatalf("%+v", s.Status())
	}
}

// A box that boots before its uplink is up must keep trying until it connects.
func TestStartRetriesAFailedConnect(t *testing.T) {
	s, c, _ := newService(t)
	s.retryDelay = time.Millisecond
	c.account = me
	c.connErrs = []error{errors.New("dial tcp: no route to host"), errors.New("dns: no such host")}
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return c.count("Connect") == 3 })
}

// The same message delivered by history (phone-number chat) and live (LID chat) is one row, one chat.
func TestLiveLIDAndHistoryPNStoredOnce(t *testing.T) {
	s, c, _ := newService(t)
	c.account = me
	s.Start(ctx)
	lid := types.NewJID("98765432109876", types.HiddenUserServer)
	live := evt(lid, lid, "SAME", &waE2E.Message{Conversation: proto.String("hi")})
	live.Info.SenderAlt = ana
	c.emit(live)
	c.emit(&events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{
		ID: proto.String(ana.String()),
		Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
			Key:     &waCommon.MessageKey{RemoteJID: proto.String(ana.String()), ID: proto.String("SAME")},
			Message: &waE2E.Message{Conversation: proto.String("hi")},
		}}},
	}}}})
	chats, _ := s.Chats(ctx)
	if len(chats) != 1 || chats[0].JID != ana.String() || chats[0].Count != 1 {
		t.Fatalf("chats %+v", chats)
	}
}

func TestRemoveLogsOutAndKeeps(t *testing.T) {
	s, c, a := newService(t)
	c.account = me
	s.Start(ctx)
	a.Save(ctx, row("ana@s.whatsapp.net", "1", "hi", t0))
	if err := s.Remove(ctx, false, false); err != nil {
		t.Fatal(err)
	}
	if s.Status().State != "unlinked" || c.count("Logout") != 1 {
		t.Fatalf("%+v %v", s.Status(), c.Calls())
	}
	if chats, _ := a.Chats(ctx, me); len(chats) != 1 {
		t.Fatalf("kept chats lost: %+v", chats)
	}
}

func TestRemoveDeleteWipesAllAccounts(t *testing.T) {
	s, c, a := newService(t)
	c.account = me
	s.Start(ctx)
	a.Save(ctx, row("ana@s.whatsapp.net", "1", "hi", t0))
	older := row("bob@s.whatsapp.net", "2", "old", t0)
	older.Account = "351900000000@s.whatsapp.net"
	a.Save(ctx, older)
	if err := s.Remove(ctx, true, false); err != nil {
		t.Fatal(err)
	}
	for _, acct := range []string{me, older.Account} {
		if chats, _ := a.Chats(ctx, acct); len(chats) != 0 {
			t.Fatalf("%s: %+v", acct, chats)
		}
	}
}

func TestRemoveLogoutFailsChangesNothing(t *testing.T) {
	s, c, a := newService(t)
	c.account = me
	c.logoutErr = errors.New("websocket not connected")
	s.Start(ctx)
	a.Save(ctx, row("ana@s.whatsapp.net", "1", "hi", t0))
	if err := s.Remove(ctx, true, false); !errors.Is(err, ErrLogout) {
		t.Fatalf("%v", err)
	}
	if chats, _ := a.Chats(ctx, me); len(chats) != 1 || c.Account() != me || c.count("Forget") != 0 || s.Status().State == "unlinked" {
		t.Fatalf("changed: %+v %v %+v", chats, c.Calls(), s.Status())
	}
}

func TestRemoveForceForgetsLocally(t *testing.T) {
	s, c, _ := newService(t)
	c.account = me
	c.logoutErr = errors.New("websocket not connected")
	s.Start(ctx)
	if err := s.Remove(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	if c.count("Forget") != 1 || c.Account() != "" || s.Status().State != "unlinked" {
		t.Fatalf("%v %+v", c.Calls(), s.Status())
	}
}

func TestRemoveWhenUnlinkedOrLoggedOut(t *testing.T) {
	s, c, a := newService(t)
	a.SetState(ctx, "logged_out")
	s.Start(ctx)
	for range 2 {
		if err := s.Remove(ctx, true, false); err != nil {
			t.Fatal(err)
		}
	}
	if c.count("Logout") != 0 || s.Status().State != "unlinked" {
		t.Fatalf("%v %+v", c.Calls(), s.Status())
	}
	if st, _ := a.State(ctx); st != "" {
		t.Fatalf("logged_out kept: %q", st)
	}
}
