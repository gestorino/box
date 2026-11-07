package whatsapp

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/listen/listentest"
)

const ownerPhone = "351912345678"

var owner = types.NewJID(ownerPhone, types.DefaultUserServer)

// listening opens a listener as whatsappd serves it and waits for caught_up.
func listening(t *testing.T, a *Archive, req *listenv1.ListenRequest) <-chan *listenv1.Event {
	t.Helper()
	hub := listen.NewHub(listen.MatchWhatsApp)
	a.SetHub(hub)
	srv := listentest.Serve(t, NewListen(a, hub))
	c, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	evs := listen.NewClient(srv.Socket).Listen(c, req)
	select {
	case e := <-evs:
		if e.GetCaughtUp() == nil {
			t.Fatalf("first event %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no caught_up")
	}
	return evs
}

func text(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }

func silent(t *testing.T, evs <-chan *listenv1.Event) {
	t.Helper()
	select {
	case e := <-evs:
		t.Fatalf("got %v", e)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestOnlyTheOwnersPrivateChatListens(t *testing.T) {
	s, c, a := newService(t)
	c.account = me
	_ = s
	evs := listening(t, a, &listenv1.ListenRequest{From: ownerPhone})

	lid := types.NewJID("98765432109876", types.HiddenUserServer)
	fromMe := evt(owner, owner, "ME", text("mine"))
	fromMe.Info.IsFromMe = true
	nothing := []*events.Message{
		evt(group, owner, "G", text("in a group")),
		evt(lid, lid, "L", text("a lid chat")),
		fromMe,
		evt(owner, owner, "NOTEXT", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}), // media, no caption
		evt(types.NewJID("351900000000", types.DefaultUserServer), owner, "OTHER", text("someone else")),
	}
	for _, e := range nothing {
		c.emit(e)
	}
	silent(t, evs)

	dup := evt(owner, owner, "DUP", text("once"))
	c.emit(dup)
	select {
	case e := <-evs:
		w := e.GetMessage().GetWhatsapp()
		if w.GetPhone() != ownerPhone || w.GetText() != "once" || w.GetChat() != owner.String() || e.GetMessage().GetId() == 0 {
			t.Fatalf("event %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the listener got nothing")
	}
	c.emit(dup) // a duplicate Save
	silent(t, evs)
}

func TestHistoryBackfillPublishesNothing(t *testing.T) {
	_, c, a := newService(t)
	c.account = me
	evs := listening(t, a, &listenv1.ListenRequest{From: ownerPhone})
	c.emit(&events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{
		ID: proto.String(owner.String()),
		Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
			Key:     &waCommon.MessageKey{RemoteJID: proto.String(owner.String()), ID: proto.String("OLD")},
			Message: text("old chat"),
		}}},
	}}}})
	silent(t, evs)
	if msgs, _ := a.Messages(ctx, me, owner.String(), 10); len(msgs) != 1 {
		t.Fatalf("history not stored: %+v", msgs)
	}
}

func TestWhatsAppReplaysSince(t *testing.T) {
	a, _ := openTemp(t)
	now := t0
	a.now = func() time.Time { return now }
	in := func(id, chat string, fromMe bool, txt string) {
		r := row(chat, id, txt, t0)
		r.FromMe = fromMe
		if _, _, err := a.Save(ctx, r); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	in("1", owner.String(), false, "before")
	in("2", owner.String(), false, "first")
	in("3", owner.String(), true, "mine")
	in("4", group.String(), false, "group")
	in("5", "98765@lid", false, "lid")
	in("6", owner.String(), false, "")
	in("7", owner.String(), false, "second")
	got, err := a.Since(ctx, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].GetWhatsapp().GetText() != "first" || got[1].GetWhatsapp().GetText() != "second" ||
		got[0].GetId() >= got[1].GetId() || got[0].GetWhatsapp().GetPhone() != ownerPhone {
		t.Fatalf("got %v", got)
	}
}

func TestAWhatsAppListenerFiltersOnFromOnly(t *testing.T) {
	a, _ := openTemp(t)
	h := NewListen(a, listen.NewHub(listen.MatchWhatsApp))
	for _, req := range []*listenv1.ListenRequest{{To: "a@b.example"}, {SubjectContains: "x"}} {
		err := h.Listen(ctx, connect.NewRequest(req), nil)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("req %v: err %v", req, err)
		}
	}
}
