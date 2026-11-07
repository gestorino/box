package whatsapp

import (
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

var self = types.NewJID("351910000000", types.DefaultUserServer)

// connected is a service on the linked account, started and connected.
func connected(t *testing.T) (*Service, *fakeClient, *Archive) {
	t.Helper()
	s, c, a := newService(t)
	c.account = me
	s.Start(ctx)
	c.emit(&events.Connected{})
	return s, c, a
}

func incoming(chat, sender types.JID, id string, at time.Time) *events.Message {
	e := evt(chat, sender, id, &waE2E.Message{Conversation: proto.String("hi")})
	e.Info.Timestamp = at
	return e
}

func unread(t *testing.T, a *Archive) int {
	t.Helper()
	n, err := a.Unread(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func pending(t *testing.T, a *Archive, account string) []ReadMark {
	t.Helper()
	marks, err := a.PendingMarks(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	return marks
}

func markedRead(c *fakeClient) []string {
	var out []string
	for _, call := range c.Calls() {
		if strings.HasPrefix(call, "MarkRead ") {
			out = append(out, call)
		}
	}
	return out
}

func TestLiveIncomingPrivateIsUnread(t *testing.T) {
	s, c, a := connected(t)
	c.emit(incoming(ana, ana, "M1", t0))
	if n := unread(t, a); n != 1 {
		t.Fatalf("unread %d", n)
	}
	chats, _ := s.Chats(ctx)
	if len(chats) != 1 || chats[0].Unread != 1 {
		t.Fatalf("chats %+v", chats)
	}
	rec := call(NewHandler(s, NewListen(s.archive, nil)), "GET", "/whatsapp/unread", "")
	if strings.TrimSpace(rec.Body.String()) != `{"count":1}` {
		t.Fatalf("unread %s", rec.Body)
	}
}

func TestHistoryArrivesRead(t *testing.T) {
	_, c, a := connected(t)
	c.emit(&events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{
		ID: proto.String(ana.String()),
		Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
			Key:     &waCommon.MessageKey{RemoteJID: proto.String(ana.String()), ID: proto.String("H1")},
			Message: &waE2E.Message{Conversation: proto.String("hi")},
		}}},
	}}}})
	if msgs, _ := a.Messages(ctx, me, ana.String(), 10); len(msgs) != 1 {
		t.Fatalf("history not stored: %v", msgs)
	}
	if n := unread(t, a); n != 0 {
		t.Fatalf("unread %d", n)
	}
}

func TestGroupAndOwnChatNeverCount(t *testing.T) {
	_, c, a := connected(t)
	c.emit(incoming(group, ana, "G1", t0))
	c.emit(incoming(types.StatusBroadcastJID, ana, "B1", t0))
	c.emit(incoming(self, self, "O1", t0))
	fromMe := incoming(ana, self, "F1", t0)
	fromMe.Info.IsFromMe = true
	c.emit(fromMe)
	if n := unread(t, a); n != 0 {
		t.Fatalf("unread %d", n)
	}
}

func TestOpeningMarksAndTellsWhatsApp(t *testing.T) {
	s, c, a := connected(t)
	c.emit(incoming(ana, ana, "M1", t0))
	c.emit(incoming(ana, ana, "M2", t0.Add(time.Second)))
	rec := call(NewHandler(s, NewListen(s.archive, nil)), "POST", "/whatsapp/read", `{"chat":"`+ana.String()+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	if n := unread(t, a); n != 0 {
		t.Fatalf("unread %d", n)
	}
	eventually(t, func() bool { return len(markedRead(c)) == 1 })
	if got := markedRead(c); got[0] != "MarkRead "+ana.String()+" M1,M2" {
		t.Fatalf("calls %v", c.Calls())
	}
	eventually(t, func() bool { return len(pending(t, a, me)) == 0 })
}

func TestOpeningAGroupTellsNothing(t *testing.T) {
	s, c, _ := connected(t)
	c.emit(incoming(group, ana, "G1", t0))
	rec := call(NewHandler(s, NewListen(s.archive, nil)), "POST", "/whatsapp/read", `{"chat":"`+group.String()+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	time.Sleep(50 * time.Millisecond) // a flush, were there one, would be on its way
	if got := markedRead(c); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}
}

func TestOpeningNeverContactsWhatsAppInTheRequest(t *testing.T) {
	s, c, a := connected(t)
	c.emit(incoming(ana, ana, "M1", t0))
	c.stickMarks(true)
	done := make(chan error, 1)
	go func() { done <- s.MarkRead(ctx, ana.String()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("MarkRead waited on WhatsApp")
	}
	if n := unread(t, a); n != 0 {
		t.Fatalf("unread %d", n)
	}
	c.stickMarks(false)
}

func TestAStuckMarkDoesNotStopALaterFlush(t *testing.T) {
	s, c, a := newService(t)
	c.account = me
	s.markTimeout = 50 * time.Millisecond
	s.Start(ctx)
	c.emit(&events.Connected{})
	c.emit(incoming(ana, ana, "M1", t0))
	c.stickMarks(true)
	s.MarkRead(ctx, ana.String())
	time.Sleep(200 * time.Millisecond) // the background flush sticks, then times out
	c.stickMarks(false)
	flushed := make(chan struct{})
	go func() { s.flushMarks(ctx); close(flushed) }()
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("a stuck send held the flush")
	}
	if got := markedRead(c); len(got) != 1 {
		t.Fatalf("calls %v", got)
	}
	if p := pending(t, a, me); len(p) != 0 {
		t.Fatalf("pending %+v", p)
	}
}

func TestAMarkThatFailsIsRetried(t *testing.T) {
	s, c, a := connected(t)
	c.emit(incoming(ana, ana, "M1", t0))
	c.failMarks(errors.New("offline"))
	if err := s.MarkRead(ctx, ana.String()); err != nil {
		t.Fatal(err)
	}
	if n := unread(t, a); n != 0 {
		t.Fatalf("unread %d", n)
	}
	if p := pending(t, a, me); len(p) != 1 || p[0].Chat != ana.String() || len(p[0].IDs) != 1 || p[0].IDs[0] != "M1" {
		t.Fatalf("pending %+v", p)
	}
	c.failMarks(nil)
	s.flushMarks(ctx)
	s.flushMarks(ctx)
	if got := markedRead(c); len(got) != 1 {
		t.Fatalf("calls %v", got)
	}
	if p := pending(t, a, me); len(p) != 0 {
		t.Fatalf("pending %+v", p)
	}
}

func TestPendingMarksSurviveARestart(t *testing.T) {
	a, path := openTemp(t)
	c := newFakeClient()
	c.account = me
	s := NewService(c, a, func() time.Time { return t0 })
	s.Start(ctx)
	c.emit(&events.Connected{})
	c.emit(incoming(ana, ana, "M1", t0))
	c.failMarks(errors.New("offline"))
	s.MarkRead(ctx, ana.String())
	a.Close()

	a2, err := OpenArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a2.Close() })
	c2 := newFakeClient()
	c2.account = me
	s2 := NewService(c2, a2, func() time.Time { return t0 })
	s2.Start(ctx)
	c2.emit(&events.Connected{}) // nudges the flush
	eventually(t, func() bool { return len(markedRead(c2)) == 1 })
}

func TestOldAccountsMarksStayBehind(t *testing.T) {
	s, c, a := newService(t)
	a.Save(ctx, Row{Account: me, ChatJID: ana.String(), ID: "M1", SenderJID: ana.String(), SentAt: t0, Kind: "text", Unread: true})
	if err := a.MarkChatRead(ctx, me, ana.String()); err != nil {
		t.Fatal(err)
	}
	other := "351920000000@s.whatsapp.net"
	c.account = other
	s.Start(ctx)
	c.emit(&events.Connected{})
	s.flushMarks(ctx)
	if p := pending(t, a, other); len(p) != 0 {
		t.Fatalf("pending %+v", p)
	}
	if got := markedRead(c); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}
	if p := pending(t, a, me); len(p) != 1 {
		t.Fatalf("the old account's mark was lost: %+v", p)
	}
}

func TestOwnDeviceReceiptClears(t *testing.T) {
	for _, typ := range []types.ReceiptType{types.ReceiptTypeRead, types.ReceiptTypeReadSelf} {
		_, c, a := connected(t)
		c.emit(incoming(ana, ana, "M1", t0))
		c.emit(&events.Receipt{
			MessageSource: types.MessageSource{Chat: ana, Sender: self, IsFromMe: true},
			MessageIDs:    []string{"M1"}, Type: typ,
		})
		if n := unread(t, a); n != 0 {
			t.Fatalf("%s: unread %d", typ, n)
		}
		if got := markedRead(c); len(got) != 0 {
			t.Fatalf("%s: calls %v", typ, got)
		}
		if p := pending(t, a, me); len(p) != 0 {
			t.Fatalf("%s: pending %+v", typ, p)
		}
	}
}

func TestOthersReceiptDoesNot(t *testing.T) {
	_, c, a := connected(t)
	c.emit(incoming(ana, ana, "M1", t0))
	c.emit(&events.Receipt{
		MessageSource: types.MessageSource{Chat: ana, Sender: ana},
		MessageIDs:    []string{"M1"}, Type: types.ReceiptTypeRead,
	})
	if n := unread(t, a); n != 1 {
		t.Fatalf("unread %d", n)
	}
}

func TestMarkChatAsReadEventClears(t *testing.T) {
	_, c, a := connected(t)
	c.emit(incoming(ana, ana, "M1", t0))
	c.emit(incoming(ana, ana, "M2", t0.Add(2*time.Minute)))
	c.emit(&events.MarkChatAsRead{JID: ana, Timestamp: t0.Add(time.Minute),
		Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(true)}})
	if n := unread(t, a); n != 1 {
		t.Fatalf("unread %d", n)
	}
	// Marked unread elsewhere: nothing to clear.
	c.emit(&events.MarkChatAsRead{JID: ana, Timestamp: t0.Add(time.Hour),
		Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(false)}})
	if n := unread(t, a); n != 1 {
		t.Fatalf("unread %d", n)
	}
}

func TestMigrationLeavesOldMessagesRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(migrations[0] + `
		PRAGMA user_version = 1;
		INSERT INTO messages VALUES ('` + me + `', '` + ana.String() + `', 'M1', '` + ana.String() +
		`', 'Ana', 0, '2026-10-28T09:00:00Z', 'text', 'hi', '');`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	a, err := OpenArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if n := unread(t, a); n != 0 {
		t.Fatalf("unread %d", n)
	}
	if _, _, err := a.Save(ctx, Row{Account: me, ChatJID: ana.String(), ID: "M2", SenderJID: ana.String(), SentAt: t0, Kind: "text", Unread: true}); err != nil {
		t.Fatal(err)
	}
	if n := unread(t, a); n != 1 {
		t.Fatalf("unread %d", n)
	}
}
