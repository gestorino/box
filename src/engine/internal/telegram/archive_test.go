package telegram

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

var ctx = context.Background()

const me = "1000"

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "telegram.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

var t0 = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

func row(peer string, id int, text string, at time.Time) Row {
	return Row{Account: me, Peer: peer, ID: id, Sender: peer, SenderName: "Ana", SentAt: at, Kind: "text", Text: text}
}

func TestSameMessageStoredOnce(t *testing.T) {
	s, _ := openTemp(t)
	r := row("user:1", 1, "hi", t0)
	for range 2 { // backfill, then live delivery
		if err := s.Save(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	msgs, _ := s.Messages(ctx, me, "user:1", 500)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages", len(msgs))
	}
}

func TestEditOverwrites(t *testing.T) {
	s, _ := openTemp(t)
	s.Save(ctx, row("user:1", 1, "helo", t0))
	edited := row("user:1", 1, "hello", t0)
	if err := s.Edit(ctx, edited); err != nil {
		t.Fatal(err)
	}
	s.Edit(ctx, row("user:1", 99, "never stored", t0)) // absent: no-op
	msgs, _ := s.Messages(ctx, me, "user:1", 500)
	if len(msgs) != 1 || msgs[0].Text != "hello" {
		t.Fatalf("msgs %+v", msgs)
	}
}

func TestDeleteCommonIDsSparesChannels(t *testing.T) {
	s, _ := openTemp(t)
	for _, p := range []string{"user:1", "chat:2", "channel:3"} {
		s.Save(ctx, row(p, 7, "x", t0))
		s.Save(ctx, row(p, 8, "y", t0))
	}
	if err := s.DeleteCommon(ctx, me, []int{7}); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]int{"user:1": 1, "chat:2": 1, "channel:3": 2} {
		if msgs, _ := s.Messages(ctx, me, p, 500); len(msgs) != want {
			t.Errorf("%s: %d messages, want %d", p, len(msgs), want)
		}
	}
}

func TestDeleteChannelOnlyThatChannel(t *testing.T) {
	s, _ := openTemp(t)
	for _, p := range []string{"channel:3", "channel:4", "user:3"} {
		s.Save(ctx, row(p, 7, "x", t0))
	}
	if err := s.DeleteChannel(ctx, me, 3, []int{7}); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]int{"channel:3": 0, "channel:4": 1, "user:3": 1} {
		if msgs, _ := s.Messages(ctx, me, p, 500); len(msgs) != want {
			t.Errorf("%s: %d messages, want %d", p, len(msgs), want)
		}
	}
}

func TestChatWithNoMessagesLeavesList(t *testing.T) {
	s, _ := openTemp(t)
	s.PutPeer(ctx, Peer{Account: me, Key: "user:1", Name: "Ana", Kind: "private"})
	s.Save(ctx, row("user:1", 1, "hi", t0))
	s.DeleteCommon(ctx, me, []int{1})
	chats, _ := s.Chats(ctx, me, "")
	if len(chats) != 0 {
		t.Fatalf("chats %+v", chats)
	}
}

func TestChatsNewestFirstWithPreviewAndCount(t *testing.T) {
	s, _ := openTemp(t)
	s.PutPeer(ctx, Peer{Account: me, Key: "user:1", Name: "Ana", Kind: "private"})
	s.PutPeer(ctx, Peer{Account: me, Key: "chat:2", Name: "Family", Kind: "group"})
	s.Save(ctx, row("user:1", 1, "first", t0))
	s.Save(ctx, row("user:1", 2, "second", t0.Add(time.Minute)))
	s.Save(ctx, row("chat:2", 3, "family's", t0.Add(30*time.Second)))
	chats, err := s.Chats(ctx, me, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 || chats[0].Peer != "user:1" || chats[0].Name != "Ana" || chats[0].Kind != "private" ||
		chats[0].Count != 2 || chats[0].LastText != "second" || chats[0].LastKind != "text" ||
		chats[0].LastAt != t0.Add(time.Minute).Format(time.RFC3339) ||
		chats[1].Peer != "chat:2" || chats[1].Name != "Family" || chats[1].Kind != "group" {
		t.Fatalf("chats %+v", chats)
	}
}

func TestMessagesOldestFirstNewest500(t *testing.T) {
	s, _ := openTemp(t)
	for i := range 600 {
		s.Save(ctx, row("user:1", i+1, "m", t0.Add(time.Duration(i)*time.Second)))
	}
	msgs, _ := s.Messages(ctx, me, "user:1", 500)
	if len(msgs) != 500 || msgs[0].ID != 101 || msgs[499].ID != 600 {
		t.Fatalf("got %d, first %d, last %d", len(msgs), msgs[0].ID, msgs[len(msgs)-1].ID)
	}
}

func TestAccountsNeverMix(t *testing.T) {
	s, _ := openTemp(t)
	s.Save(ctx, row("user:1", 1, "mine", t0))
	other := row("user:1", 1, "theirs", t0)
	other.Account = "2000"
	s.Save(ctx, other)
	mine, _ := s.Messages(ctx, me, "user:1", 500)
	theirs, _ := s.Messages(ctx, "2000", "user:1", 500)
	if len(mine) != 1 || mine[0].Text != "mine" || len(theirs) != 1 || theirs[0].Text != "theirs" {
		t.Fatalf("mine %+v theirs %+v", mine, theirs)
	}
}

func TestPeerNameKeptUntilRenamed(t *testing.T) {
	s, _ := openTemp(t)
	s.PutPeer(ctx, Peer{Account: me, Key: "user:1", AccessHash: 42, Name: "Ana", Username: "ana", Kind: "private"})
	s.PutPeer(ctx, Peer{Account: me, Key: "user:1", Kind: "private"}) // an entity without a name or hash
	p, ok, _ := s.Peer(ctx, me, "user:1")
	if !ok || p.Name != "Ana" || p.Username != "ana" || p.AccessHash != 42 {
		t.Fatalf("peer %+v", p)
	}
	s.PutPeer(ctx, Peer{Account: me, Key: "user:1", Name: "Ana S.", Kind: "private"})
	if p, _, _ := s.Peer(ctx, me, "user:1"); p.Name != "Ana S." || p.AccessHash != 42 {
		t.Fatalf("renamed %+v", p)
	}
	if _, ok, _ := s.Peer(ctx, me, "user:404"); ok {
		t.Fatal("unknown peer found")
	}
}

func TestDeleteAccountOnlyThatAccount(t *testing.T) {
	s, _ := openTemp(t)
	s.Save(ctx, row("user:1", 1, "mine", t0))
	s.PutPeer(ctx, Peer{Account: me, Key: "user:1", Name: "Ana", Kind: "private"})
	other := row("user:1", 1, "theirs", t0)
	other.Account = "2000"
	s.Save(ctx, other)
	if err := s.DeleteAccount(ctx, me); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := s.Messages(ctx, me, "user:1", 500); len(msgs) != 0 {
		t.Fatal("mine kept")
	}
	if _, ok, _ := s.Peer(ctx, me, "user:1"); ok {
		t.Fatal("my peer kept")
	}
	if msgs, _ := s.Messages(ctx, "2000", "user:1", 500); len(msgs) != 1 {
		t.Fatal("theirs deleted")
	}
}

func TestConfigSurvivesReopen(t *testing.T) {
	s, path := openTemp(t)
	if v, _ := s.Get(ctx, "api_id"); v != "" {
		t.Fatalf("unset key = %q", v)
	}
	s.Set(ctx, "api_id", "12345")
	s.Set(ctx, "link", "logged_out")
	s.Close()
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v, _ := s2.Get(ctx, "api_id"); v != "12345" {
		t.Fatalf("api_id = %q", v)
	}
	if v, _ := s2.Get(ctx, "link"); v != "logged_out" {
		t.Fatalf("link = %q", v)
	}
}
