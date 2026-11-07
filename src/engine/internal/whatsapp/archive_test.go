package whatsapp

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

var ctx = context.Background()

const me = "351910000000@s.whatsapp.net"

func openTemp(t *testing.T) (*Archive, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.db")
	a, err := OpenArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a, path
}

var t0 = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

func row(chat, id, text string, at time.Time) Row {
	return Row{Account: me, ChatJID: chat, ID: id, SenderJID: chat, SenderName: "Ana", SentAt: at, Kind: "text", Text: text}
}

func TestSameMessageStoredOnce(t *testing.T) {
	a, _ := openTemp(t)
	r := row("ana@s.whatsapp.net", "M1", "hi", t0)
	for range 2 { // history sync, then live delivery
		if _, _, err := a.Save(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	msgs, _ := a.Messages(ctx, me, "ana@s.whatsapp.net", 500)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages", len(msgs))
	}
}

func TestChatsNewestFirstWithPreviewAndCount(t *testing.T) {
	a, _ := openTemp(t)
	a.Save(ctx, row("ana@s.whatsapp.net", "1", "first", t0))
	a.Save(ctx, row("ana@s.whatsapp.net", "2", "second", t0.Add(time.Minute)))
	a.Save(ctx, row("bob@s.whatsapp.net", "3", "bob's", t0.Add(30*time.Second)))
	chats, err := a.Chats(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 || chats[0].JID != "ana@s.whatsapp.net" || chats[0].Count != 2 || chats[0].LastText != "second" ||
		chats[0].LastKind != "text" || chats[0].LastAt != t0.Add(time.Minute).Format(time.RFC3339) || chats[1].JID != "bob@s.whatsapp.net" {
		t.Fatalf("chats %+v", chats)
	}
}

func TestMessagesOldestFirstNewest500(t *testing.T) {
	a, _ := openTemp(t)
	for i := range 600 {
		a.Save(ctx, row("ana@s.whatsapp.net", fmt.Sprint(i), fmt.Sprint(i), t0.Add(time.Duration(i)*time.Second)))
	}
	msgs, _ := a.Messages(ctx, me, "ana@s.whatsapp.net", 500)
	if len(msgs) != 500 || msgs[0].Text != "100" || msgs[499].Text != "599" {
		t.Fatalf("got %d, first %q, last %q", len(msgs), msgs[0].Text, msgs[len(msgs)-1].Text)
	}
}

func TestAccountsNeverMix(t *testing.T) {
	a, _ := openTemp(t)
	a.Save(ctx, row("ana@s.whatsapp.net", "1", "to me", t0))
	other := row("ana@s.whatsapp.net", "1", "to the other number", t0)
	other.Account = "351920000000@s.whatsapp.net"
	a.Save(ctx, other)
	mine, _ := a.Messages(ctx, me, "ana@s.whatsapp.net", 500)
	if len(mine) != 1 || mine[0].Text != "to me" {
		t.Fatalf("mine %+v", mine)
	}
	if chats, _ := a.Chats(ctx, other.Account); len(chats) != 1 || chats[0].LastText != "to the other number" {
		t.Fatalf("other %+v", chats)
	}
}

func TestDeleteAccountOnlyThatAccount(t *testing.T) {
	a, _ := openTemp(t)
	a.Save(ctx, row("ana@s.whatsapp.net", "1", "keep? no", t0))
	other := row("ana@s.whatsapp.net", "2", "keep", t0)
	other.Account = "351920000000@s.whatsapp.net"
	a.Save(ctx, other)
	if err := a.DeleteAccount(ctx, me); err != nil {
		t.Fatal(err)
	}
	if chats, _ := a.Chats(ctx, me); len(chats) != 0 {
		t.Fatalf("deleted account still has %+v", chats)
	}
	if chats, _ := a.Chats(ctx, other.Account); len(chats) != 1 {
		t.Fatalf("other account touched: %+v", chats)
	}
}

func TestEditUpdatesTheRow(t *testing.T) {
	a, _ := openTemp(t)
	a.Save(ctx, row("ana@s.whatsapp.net", "1", "typo", t0))
	if err := a.Update(ctx, me, "ana@s.whatsapp.net", "1", "fixed"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := a.Messages(ctx, me, "ana@s.whatsapp.net", 500)
	if len(msgs) != 1 || msgs[0].Text != "fixed" {
		t.Fatalf("%+v", msgs)
	}
}

func TestChatNameKeptUntilRenamed(t *testing.T) {
	a, _ := openTemp(t)
	a.Save(ctx, row("g1@g.us", "1", "hello group", t0))
	a.NameChat(ctx, me, "g1@g.us", "Family", true)
	a.NameChat(ctx, me, "g1@g.us", "", true) // an event without a name must not erase it
	chats, _ := a.Chats(ctx, me)
	if chats[0].Name != "Family" || !chats[0].IsGroup {
		t.Fatalf("%+v", chats[0])
	}
	a.NameChat(ctx, me, "g1@g.us", "Family 2026", true)
	chats, _ = a.Chats(ctx, me)
	if chats[0].Name != "Family 2026" {
		t.Fatalf("%+v", chats[0])
	}
}

func TestStateSurvivesReopen(t *testing.T) {
	a, path := openTemp(t)
	a.Save(ctx, row("ana@s.whatsapp.net", "1", "hi", t0))
	if err := a.SetState(ctx, "logged_out"); err != nil {
		t.Fatal(err)
	}
	a.Close()
	b, err := OpenArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if s, _ := b.State(ctx); s != "logged_out" {
		t.Fatalf("state %q", s)
	}
	if msgs, _ := b.Messages(ctx, me, "ana@s.whatsapp.net", 500); len(msgs) != 1 {
		t.Fatalf("messages lost on reopen")
	}
}
