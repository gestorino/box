package telegram

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

// withDialogs gives the fake account four chats: a private chat, a basic group, a supergroup and a
// broadcast channel, each with history.
func withDialogs(f *fake) {
	f.with(func(f *fake) {
		f.dialogs = []Dialog{
			{Peer: Peer{Key: "user:1", AccessHash: 11, Name: "Ana Silva", Kind: "private"}},
			{Peer: Peer{Key: "chat:2", Name: "Family", Kind: "group"}},
			{Peer: Peer{Key: "channel:3", AccessHash: 33, Name: "Neighbours", Kind: "group"}, Pts: 300},
			{Peer: Peer{Key: "channel:4", AccessHash: 44, Name: "News", Kind: "channel"}, Pts: 400},
		}
		for i := 150; i > 0; i-- { // newest first, as messages.getHistory answers
			f.history["user:1"] = append(f.history["user:1"], msg(&tg.PeerUser{UserID: 1}, i, "m"))
		}
		f.history["chat:2"] = []tg.MessageClass{msg(&tg.PeerChat{ChatID: 2}, 1, "family")}
		f.history["channel:3"] = []tg.MessageClass{msg(&tg.PeerChannel{ChannelID: 3}, 1, "neighbours")}
		f.history["channel:4"] = []tg.MessageClass{msg(&tg.PeerChannel{ChannelID: 4}, 1, "news")}
		f.historyEnt = ents
	})
}

func asked(f *fake) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

func count(s *Store, peer string) int {
	msgs, _ := s.Messages(ctx, me, peer, 500)
	return len(msgs)
}

func backfilled(t *testing.T) (*fake, *Store, *Service) {
	t.Helper()
	s, _ := openTemp(t)
	f := newFake()
	withDialogs(f)
	svc := linked(t, f, s)
	eventually(t, "backfill", func() bool { v, _ := s.Get(ctx, "backfilled"); return v == "1" })
	return f, s, svc
}

func TestBackfillSeedsChannelState(t *testing.T) {
	f, s, _ := backfilled(t)
	if got := asked(f); !slices.Equal(got, []string{"user:1", "chat:2", "channel:3"}) {
		t.Fatalf("history asked for %v", got)
	}
	if n := count(s, "user:1"); n != 100 {
		t.Fatalf("user:1 holds %d, want the newest 100", n)
	}
	u := NewUpdateState(s)
	if pts, found, _ := u.GetChannelPts(ctx, 1000, 3); !found || pts != 300 {
		t.Fatalf("supergroup pts %d found=%v", pts, found)
	}
	if h, found, _ := u.GetChannelAccessHash(ctx, 1000, 3); !found || h != 33 {
		t.Fatalf("supergroup hash %d", h)
	}
	if _, found, _ := u.GetChannelPts(ctx, 1000, 4); found {
		t.Fatal("broadcast channel tracked while the switch is off")
	}
	if p, _, _ := s.Peer(ctx, me, "chat:2"); p.Name != "Family" {
		t.Fatalf("peer %+v", p)
	}
}

func TestBackfillOnlyOnce(t *testing.T) {
	f, s, svc := backfilled(t)
	n := len(asked(f))
	f.emit(Connected{}) // a reconnect
	svc.Stop()
	svc = newService(t, f, s) // a restart
	f.emit(Connected{})
	if got := len(asked(f)); got != n {
		t.Fatalf("history asked %d times, want %d", got, n)
	}
}

func TestBroadcastSkippedWhileSwitchOff(t *testing.T) {
	f, s, _ := backfilled(t)
	f.emit(NewMessage{Msg: msg(&tg.PeerChannel{ChannelID: 4}, 2, "more news"), Ent: ents})
	f.emit(NewMessage{Msg: msg(&tg.PeerChannel{ChannelID: 3}, 2, "more neighbours"), Ent: ents})
	if count(s, "channel:4") != 0 || count(s, "channel:3") != 2 {
		t.Fatalf("channel:4 %d, channel:3 %d", count(s, "channel:4"), count(s, "channel:3"))
	}
}

func TestArchiveChannelsToggle(t *testing.T) {
	f, s, svc := backfilled(t)
	if err := svc.SetArchiveChannels(ctx, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "channel backfilled", func() bool { return count(s, "channel:4") == 1 })
	if a := asked(f); a[len(a)-1] != "channel:4" || slices.Contains(a[3:], "user:1") {
		t.Fatalf("history asked for %v", a)
	}
	u := NewUpdateState(s)
	if pts, found, _ := u.GetChannelPts(ctx, 1000, 4); !found || pts != 400 {
		t.Fatalf("channel pts %d found=%v", pts, found)
	}
	f.emit(NewMessage{Msg: msg(&tg.PeerChannel{ChannelID: 4}, 2, "more news"), Ent: ents})
	if count(s, "channel:4") != 2 || !svc.Status().ArchiveChannels {
		t.Fatal("live post not archived with the switch on")
	}

	if err := svc.SetArchiveChannels(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := u.GetChannelPts(ctx, 1000, 4); found {
		t.Fatal("channel still tracked")
	}
	if _, found, _ := u.GetChannelPts(ctx, 1000, 3); !found {
		t.Fatal("supergroup dropped with the channels")
	}
	chats, _ := svc.Chats(ctx)
	if !slices.ContainsFunc(chats, func(c Chat) bool { return c.Peer == "channel:4" }) {
		t.Fatal("stored channel left the list")
	}
	if v, _ := s.Get(ctx, "archive_channels"); v != "" {
		t.Fatalf("persisted %q", v)
	}
}

func TestLiveNewEditDelete(t *testing.T) {
	f, s, svc := backfilled(t)
	f.emit(NewMessage{Msg: msg(&tg.PeerUser{UserID: 5}, 500, "helo"), Ent: ents})
	f.emit(EditMessage{Msg: msg(&tg.PeerUser{UserID: 5}, 500, "hello"), Ent: ents})
	msgs, _ := svc.Messages(ctx, "user:5")
	if len(msgs) != 1 || msgs[0].Text != "hello" || msgs[0].SenderName != "@bob" {
		t.Fatalf("msgs %+v", msgs)
	}
	f.emit(DeletedCommon{IDs: []int{500}})
	f.emit(DeletedChannel{ChannelID: 3, IDs: []int{1}})
	if count(s, "user:5") != 0 || count(s, "channel:3") != 0 || count(s, "chat:2") != 1 {
		t.Fatal("deletes not applied")
	}
}

func TestTooLongBackfillsAgainDeduped(t *testing.T) {
	f, s, _ := backfilled(t)
	n := len(asked(f))
	f.emit(TooLong{})
	eventually(t, "common backfill", func() bool { return len(asked(f)) == n+2 })
	if a := asked(f)[n:]; !slices.Equal(a, []string{"user:1", "chat:2"}) {
		t.Fatalf("asked %v", a)
	}
	f.emit(TooLong{ChannelID: 3})
	eventually(t, "channel backfill", func() bool { return len(asked(f)) == n+3 })
	if count(s, "user:1") != 100 || count(s, "channel:3") != 1 {
		t.Fatal("backfill stored duplicates")
	}
}

func TestRelinkBackfillsAgain(t *testing.T) {
	f, s, svc := backfilled(t)
	n := len(asked(f))
	if err := svc.Unlink(ctx, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "running", func() bool { return running(f) })
	svc.Link(ctx)
	waitState(t, svc, "connecting")
	f.emit(Connected{})
	eventually(t, "second backfill", func() bool { return len(asked(f)) == 2*n })
	eventually(t, "copied again", func() bool { return count(s, "user:1") == 100 })
}

func TestFailedBackfillRetriedOnNextConnect(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	withDialogs(f)
	f.historyErr = map[string]error{"chat:2": errors.New("connection reset")}
	linked(t, f, s)
	eventually(t, "first attempt", func() bool { return len(asked(f)) == 3 })
	time.Sleep(50 * time.Millisecond)
	if v, _ := s.Get(ctx, "backfilled"); v != "" || count(s, "chat:2") != 0 {
		t.Fatalf("a failed backfill marked done (%q)", v)
	}
	f.emit(Connected{})
	eventually(t, "retried", func() bool { v, _ := s.Get(ctx, "backfilled"); return v == "1" })
	if count(s, "chat:2") != 1 {
		t.Fatal("chat:2 never copied")
	}
}

func TestOneBackfillAtATime(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	withDialogs(f)
	hold := make(chan struct{})
	f.historyHold = hold
	linked(t, f, s)
	f.emit(Connected{}) // a reconnect while the first backfill is still running
	time.Sleep(50 * time.Millisecond)
	close(hold)
	eventually(t, "backfill", func() bool { v, _ := s.Get(ctx, "backfilled"); return v == "1" })
	time.Sleep(50 * time.Millisecond)
	if got := asked(f); !slices.Equal(got, []string{"user:1", "chat:2", "channel:3"}) {
		t.Fatalf("history asked for %v", got)
	}
}

func TestNewGroupBackfilledOnce(t *testing.T) {
	f, s, _ := backfilled(t)
	joined := tg.Entities{
		Users:    map[int64]*tg.User{5: {ID: 5, Username: "bob"}, 6: {ID: 6, FirstName: "Eve"}},
		Channels: map[int64]*tg.Channel{9: {ID: 9, Title: "Book club", Megagroup: true, AccessHash: 99}},
	}
	f.with(func(f *fake) {
		f.history["channel:9"] = []tg.MessageClass{msg(&tg.PeerChannel{ChannelID: 9}, 2, "older"), msg(&tg.PeerChannel{ChannelID: 9}, 1, "oldest")}
	})
	n := len(asked(f))
	f.emit(NewMessage{Msg: msg(&tg.PeerChannel{ChannelID: 9}, 3, "welcome"), Ent: joined})
	eventually(t, "the new group's history", func() bool { return count(s, "channel:9") == 3 })
	f.emit(NewMessage{Msg: msg(&tg.PeerChannel{ChannelID: 9}, 4, "again"), Ent: joined})
	f.emit(NewMessage{Msg: msg(&tg.PeerUser{UserID: 6}, 5, "a new private chat"), Ent: joined})
	time.Sleep(50 * time.Millisecond)
	if got := asked(f)[n:]; !slices.Equal(got, []string{"channel:9"}) {
		t.Fatalf("history asked for %v", got)
	}
}

func TestChannelTooLongSkipsTheDialogList(t *testing.T) {
	f, _, _ := backfilled(t)
	f.mu.Lock()
	runs := f.dialogRuns
	f.mu.Unlock()
	n := len(asked(f))
	f.emit(TooLong{ChannelID: 3})
	eventually(t, "channel refetched", func() bool { return len(asked(f)) == n+1 })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dialogRuns != runs || f.asked[n] != "channel:3" {
		t.Fatalf("dialog list paged %d more times; asked %v", f.dialogRuns-runs, f.asked[n:])
	}
}

func TestSwitchOffRestartsTheRun(t *testing.T) {
	f, _, svc := backfilled(t)
	svc.SetArchiveChannels(ctx, true)
	f.mu.Lock()
	runs := len(f.runs)
	f.mu.Unlock()
	if err := svc.SetArchiveChannels(ctx, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a fresh run, so gotd forgets the channels it polls", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.runs) == runs+1
	})
}
