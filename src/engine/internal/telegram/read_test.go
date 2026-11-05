package telegram

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const selfKey = "user:1000"

func putPeer(t *testing.T, s *Store, key, kind string) {
	t.Helper()
	if err := s.PutPeer(ctx, Peer{Account: me, Key: key, AccessHash: 7, Name: key, Kind: kind}); err != nil {
		t.Fatal(err)
	}
}

func save(t *testing.T, s *Store, r Row) {
	t.Helper()
	if err := s.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
}

func unread(t *testing.T, s *Store) int {
	t.Helper()
	n, err := s.Unread(ctx, me, selfKey)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func readCalls(f *fake) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.read...)
}

func TestPrivateIncomingAboveTheMarkerCounts(t *testing.T) {
	s, _ := openTemp(t)
	putPeer(t, s, "user:1", "private")
	putPeer(t, s, selfKey, "private")
	putPeer(t, s, "chat:5", "group")
	putPeer(t, s, "channel:6", "channel")
	save(t, s, row("user:1", 41, "a", t0))
	save(t, s, row("user:1", 42, "b", t0))
	mine := row("user:1", 43, "mine", t0)
	mine.FromMe = true
	save(t, s, mine)
	save(t, s, row(selfKey, 44, "note", t0))
	save(t, s, row("chat:5", 45, "g", t0))
	save(t, s, row("channel:6", 46, "c", t0))
	if n := unread(t, s); n != 2 {
		t.Fatalf("unread %d, want the two incoming private messages", n)
	}
	chats, _ := s.Chats(ctx, me, selfKey)
	for _, c := range chats {
		if want := map[string]int{"user:1": 2}[c.Peer]; c.Unread != want {
			t.Errorf("%s: unread %d, want %d", c.Peer, c.Unread, want)
		}
	}
	if err := s.ReadUpTo(ctx, me, "user:1", 41); err != nil {
		t.Fatal(err)
	}
	if n := unread(t, s); n != 1 {
		t.Fatalf("unread %d after the marker moved to 41", n)
	}
}

func TestMigrationLeavesEverythingRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telegram.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// telegram.db as it was before unread counts: the first schema, version 1.
	if _, err := old.Exec(migrations[0] + `PRAGMA user_version = 1;
		INSERT INTO peers (account, peer, kind) VALUES ('1000', 'user:1', 'private'), ('1000', 'user:2', 'private');
		INSERT INTO messages VALUES ('1000','user:1',41,'user:1','Ana',0,'2026-10-28T09:00:00Z','text','a','');
		INSERT INTO messages VALUES ('1000','user:1',42,'user:1','Ana',0,'2026-10-28T09:00:00Z','text','b','');`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := unread(t, s); n != 0 {
		t.Fatalf("unread %d after migrating", n)
	}
	save(t, s, row("user:1", 43, "new", t0))
	if n := unread(t, s); n != 1 {
		t.Fatalf("unread %d, want the new message", n)
	}
}

func TestConnectSeedsFromTheDialog(t *testing.T) {
	s, _ := openTemp(t)
	putPeer(t, s, "user:1", "private")
	for id := 41; id <= 43; id++ {
		save(t, s, row("user:1", id, "x", t0))
	}
	if err := s.ReadUpTo(ctx, me, "user:1", 43); err != nil { // as migration leaves it
		t.Fatal(err)
	}
	f := newFake()
	f.with(func(f *fake) {
		f.dialogs = []Dialog{{Peer: Peer{Key: "user:1", Kind: "private"}, ReadInboxMaxID: 41}}
	})
	svc := linked(t, f, s)
	eventually(t, "the dialog's read state taken", func() bool { return unread(t, s) == 2 })
	if n, err := svc.Unread(ctx); err != nil || n != 2 {
		t.Fatalf("service unread %d, %v", n, err)
	}
}

func TestSeedDoesNotUndoAPendingMark(t *testing.T) {
	s, _ := openTemp(t)
	putPeer(t, s, "user:1", "private")
	save(t, s, row("user:1", 42, "x", t0))
	save(t, s, row("user:1", 43, "x", t0))
	if err := s.MarkPeerRead(ctx, me, "user:1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedRead(ctx, me, "user:1", 41); err != nil {
		t.Fatal(err)
	}
	if n := unread(t, s); n != 0 {
		t.Fatalf("unread %d: the seed undid the mark", n)
	}
	if p, _ := s.PendingReads(ctx, me); len(p) != 1 || p[0].MaxID != 43 {
		t.Fatalf("pending %+v", p)
	}
}

func TestOpeningTellsTelegram(t *testing.T) {
	s, _ := openTemp(t)
	putPeer(t, s, "user:1", "private")
	save(t, s, row("user:1", 41, "x", t0))
	save(t, s, row("user:1", 42, "x", t0))
	f := newFake()
	svc := linked(t, f, s)
	if err := svc.MarkRead(ctx, "user:1"); err != nil {
		t.Fatal(err)
	}
	if n := unread(t, s); n != 0 {
		t.Fatalf("unread %d right after opening", n)
	}
	eventually(t, "Telegram told", func() bool {
		c := readCalls(f)
		return len(c) == 1 && c[0] == "ReadHistory user:1 42"
	})
	eventually(t, "the mark cleared", func() bool { p, _ := s.PendingReads(ctx, me); return len(p) == 0 })
	eventually(t, "status set offline again", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.offline >= 2 })
}

func TestOpeningAGroupTellsNothing(t *testing.T) {
	s, _ := openTemp(t)
	putPeer(t, s, "chat:5", "group")
	putPeer(t, s, "channel:6", "channel")
	putPeer(t, s, selfKey, "private")
	f := newFake()
	svc := linked(t, f, s)
	for i, key := range []string{"chat:5", "channel:6", selfKey, "user:99"} {
		save(t, s, row(key, 50+i, "x", t0))
		if err := svc.MarkRead(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if c := readCalls(f); len(c) != 0 {
		t.Fatalf("told Telegram: %v", c)
	}
	if p, _ := s.PendingReads(ctx, me); len(p) != 0 {
		t.Fatalf("pending %+v", p)
	}
}

func TestAMarkThatFailsIsRetriedOnConnect(t *testing.T) {
	s, _ := openTemp(t)
	putPeer(t, s, "user:1", "private")
	save(t, s, row("user:1", 42, "x", t0))
	f := newFake()
	f.with(func(f *fake) { f.readErr = ErrNotConnected })
	svc := linked(t, f, s)
	if err := svc.MarkRead(ctx, "user:1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if p, _ := s.PendingReads(ctx, me); len(p) != 1 {
		t.Fatalf("pending %+v: a failed mark was dropped", p)
	}
	f.with(func(f *fake) { f.readErr = nil })
	f.emit(Connected{})
	eventually(t, "retried on connect", func() bool { return len(readCalls(f)) == 1 })
	eventually(t, "the mark cleared", func() bool { p, _ := s.PendingReads(ctx, me); return len(p) == 0 })
}

func TestOldAccountsMarksStayBehind(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.PutPeer(ctx, Peer{Account: "999", Key: "user:1", Kind: "private"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, Row{Account: "999", Peer: "user:1", ID: 5, SentAt: t0, Kind: "text"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPeerRead(ctx, "999", "user:1"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PendingReads(ctx, me); len(p) != 0 {
		t.Fatalf("another account's mark: %+v", p)
	}
	if p, _ := s.PendingReads(ctx, "999"); len(p) != 1 {
		t.Fatalf("its own mark gone: %+v", p)
	}
}

func TestReadElsewhereClears(t *testing.T) {
	s, _ := openTemp(t)
	putPeer(t, s, "user:1", "private")
	save(t, s, row("user:1", 41, "x", t0))
	save(t, s, row("user:1", 42, "x", t0))
	f := newFake()
	linked(t, f, s)
	f.emit(ReadInbox{Peer: "user:1", MaxID: 42})
	eventually(t, "cleared", func() bool { return unread(t, s) == 0 })
	if c := readCalls(f); len(c) != 0 {
		t.Fatalf("told Telegram what it said itself: %v", c)
	}
}

func TestStaleReadEventDoesNotMoveBack(t *testing.T) {
	s, _ := openTemp(t)
	putPeer(t, s, "user:1", "private")
	save(t, s, row("user:1", 41, "x", t0))
	save(t, s, row("user:1", 42, "x", t0))
	if err := s.ReadUpTo(ctx, me, "user:1", 42); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadUpTo(ctx, me, "user:1", 41); err != nil {
		t.Fatal(err)
	}
	if n := unread(t, s); n != 0 {
		t.Fatalf("unread %d: the marker moved back", n)
	}
}

func TestPutPeerKeepsTheMarker(t *testing.T) {
	s, _ := openTemp(t)
	putPeer(t, s, "user:1", "private")
	save(t, s, row("user:1", 42, "x", t0))
	if err := s.ReadUpTo(ctx, me, "user:1", 42); err != nil {
		t.Fatal(err)
	}
	putPeer(t, s, "user:1", "private")
	if n := unread(t, s); n != 0 {
		t.Fatalf("unread %d: re-putting the peer reset the marker", n)
	}
}

func TestHandlerUnreadAndRead(t *testing.T) {
	s, _ := openTemp(t)
	putPeer(t, s, "user:1", "private")
	save(t, s, row("user:1", 42, "x", t0))
	f := newFake()
	h := NewHandler(linked(t, f, s))
	if rec := call(h, "GET", "/telegram/unread", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"count":1}` {
		t.Fatalf("unread: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/telegram/read", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body: %d", rec.Code)
	}
	if rec := call(h, "POST", "/telegram/read", `{"peer":"user:1"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "GET", "/telegram/unread", ""); strings.TrimSpace(rec.Body.String()) != `{"count":0}` {
		t.Fatalf("unread after: %s", rec.Body)
	}
}

func TestAStuckMarkDoesNotStopALaterFlush(t *testing.T) {
	s, _ := openTemp(t)
	putPeer(t, s, "user:1", "private")
	save(t, s, row("user:1", 42, "x", t0))
	f := newFake()
	svc := linked(t, f, s)
	svc.readTimeout = 50 * time.Millisecond
	f.with(func(f *fake) { f.readStuck = true })
	if err := svc.MarkRead(ctx, "user:1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // the background flush sticks, then times out
	f.with(func(f *fake) { f.readStuck = false })
	flushed := make(chan struct{})
	go func() { svc.flushReads(ctx); close(flushed) }()
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("a stuck send held the flush")
	}
	if c := readCalls(f); len(c) != 1 || c[0] != "ReadHistory user:1 42" {
		t.Fatalf("calls %v", c)
	}
	if p, _ := s.PendingReads(ctx, me); len(p) != 0 {
		t.Fatalf("pending %+v", p)
	}
}
