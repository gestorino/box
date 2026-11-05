package telegram

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram/updates"
)

func TestSessionNotPersistedUntilLinked(t *testing.T) {
	s, path := openTemp(t)
	ss := NewSessionStore(s)
	if _, err := ss.LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("empty load: %v", err)
	}
	ss.StoreSession(ctx, []byte("half-signed-in"))
	if b, _ := ss.LoadSession(ctx); string(b) != "half-signed-in" { // gotd reads back its own key
		t.Fatalf("in memory: %q", b)
	}
	s.Close()
	s, _ = reopen(t, path)
	if _, err := NewSessionStore(s).LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("an unlinked session reached disk: %v", err)
	}

	ss = NewSessionStore(s)
	ss.StoreSession(ctx, []byte("linked"))
	if err := ss.Persist(ctx); err != nil {
		t.Fatal(err)
	}
	ss.StoreSession(ctx, []byte("linked, rotated")) // writes through once persisted
	s.Close()
	s, _ = reopen(t, path)
	if b, err := NewSessionStore(s).LoadSession(ctx); err != nil || string(b) != "linked, rotated" {
		t.Fatalf("after persist: %q %v", b, err)
	}
}

func TestForgetClearsSession(t *testing.T) {
	s, path := openTemp(t)
	ss := NewSessionStore(s)
	ss.StoreSession(ctx, []byte("linked"))
	ss.Persist(ctx)
	if err := ss.Forget(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("memory kept: %v", err)
	}
	ss.StoreSession(ctx, []byte("next pairing")) // memory-only again
	s.Close()
	s, _ = reopen(t, path)
	if _, err := NewSessionStore(s).LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("disk kept: %v", err)
	}
}

func reopen(t *testing.T, path string) (*Store, string) {
	t.Helper()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

var (
	_ updates.StateStorage        = (*UpdateState)(nil)
	_ updates.ChannelAccessHasher = (*UpdateState)(nil)
	_ updates.UserAccessHasher    = (*UpdateState)(nil)
	_ session.Storage             = (*SessionStore)(nil)
)

func TestUpdateStateRoundTrip(t *testing.T) {
	s, path := openTemp(t)
	u := NewUpdateState(s)
	if _, found, err := u.GetState(ctx, 1000); found || err != nil {
		t.Fatalf("fresh state found=%v err=%v", found, err)
	}
	u.SetState(ctx, 1000, updates.State{Pts: 1, Qts: 2, Date: 3, Seq: 4})
	u.SetPts(ctx, 1000, 10)
	u.SetQts(ctx, 1000, 20)
	u.SetDateSeq(ctx, 1000, 30, 40)
	u.SetChannelPts(ctx, 1000, 77, 700)
	u.SetChannelPts(ctx, 1000, 88, 800)
	u.SetChannelPts(ctx, 2000, 99, 900) // another account

	s.Close()
	s, _ = reopen(t, path)
	u = NewUpdateState(s)
	if st, found, _ := u.GetState(ctx, 1000); !found || st != (updates.State{Pts: 10, Qts: 20, Date: 30, Seq: 40}) {
		t.Fatalf("state %+v found=%v", st, found)
	}
	if pts, found, _ := u.GetChannelPts(ctx, 1000, 77); !found || pts != 700 {
		t.Fatalf("channel pts %d found=%v", pts, found)
	}
	if _, found, _ := u.GetChannelPts(ctx, 1000, 99); found {
		t.Fatal("another account's channel found")
	}
	var ids []int64
	u.ForEachChannels(ctx, 1000, func(_ context.Context, id int64, _ int) error { ids = append(ids, id); return nil })
	slices.Sort(ids)
	if !slices.Equal(ids, []int64{77, 88}) {
		t.Fatalf("channels %v", ids)
	}
}

func TestAccessHashesInPeers(t *testing.T) {
	s, _ := openTemp(t)
	u := NewUpdateState(s)
	if _, found, _ := u.GetChannelAccessHash(ctx, 1000, 77); found {
		t.Fatal("unknown hash found")
	}
	u.SetChannelAccessHash(ctx, 1000, 77, 7777)
	u.SetUserAccessHash(ctx, 1000, 5, 5555)
	if h, found, _ := u.GetChannelAccessHash(ctx, 1000, 77); !found || h != 7777 {
		t.Fatalf("channel hash %d", h)
	}
	if h, found, _ := u.GetUserAccessHash(ctx, 1000, 5); !found || h != 5555 {
		t.Fatalf("user hash %d", h)
	}
	if p, ok, _ := s.Peer(ctx, "1000", "channel:77"); !ok || p.AccessHash != 7777 {
		t.Fatalf("peer %+v", p)
	}
}

func TestDropChannels(t *testing.T) {
	s, _ := openTemp(t)
	u := NewUpdateState(s)
	u.SetChannelPts(ctx, 1000, 77, 700)
	u.SetChannelPts(ctx, 1000, 88, 800)
	if err := u.DropChannels(ctx, "1000", []int64{77}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := u.GetChannelPts(ctx, 1000, 77); found {
		t.Fatal("dropped channel still tracked")
	}
	if _, found, _ := u.GetChannelPts(ctx, 1000, 88); !found {
		t.Fatal("other channel dropped")
	}
}

func TestSessionLinked(t *testing.T) {
	s, path := openTemp(t)
	ss := NewSessionStore(s)
	ss.StoreSession(ctx, []byte("k"))
	if ss.Linked() {
		t.Fatal("linked before Persist")
	}
	ss.Persist(ctx)
	if !ss.Linked() {
		t.Fatal("not linked after Persist")
	}
	s.Close()
	s, _ = reopen(t, path)
	ss = NewSessionStore(s)
	if !ss.Linked() {
		t.Fatal("not linked after a restart")
	}
	ss.Forget(ctx)
	if ss.Linked() {
		t.Fatal("linked after Forget")
	}
}

func TestBroadcastsNotTrackedWhileOff(t *testing.T) {
	s, _ := openTemp(t)
	u := NewUpdateState(s)
	s.PutPeer(ctx, Peer{Account: "1000", Key: "channel:4", Kind: "channel"})
	s.PutPeer(ctx, Peer{Account: "1000", Key: "channel:3", Kind: "group"})
	u.TrackBroadcasts(true)
	u.SetChannelPts(ctx, 1000, 4, 400) // tracked while the switch was on

	u.TrackBroadcasts(false)
	u.SetChannelPts(ctx, 1000, 3, 300)
	u.SetChannelPts(ctx, 1000, 4, 401) // gotd meeting the channel's next post
	var ids []int64
	u.ForEachChannels(ctx, 1000, func(_ context.Context, id int64, _ int) error { ids = append(ids, id); return nil })
	if !slices.Equal(ids, []int64{3}) {
		t.Fatalf("gotd would reload %v", ids)
	}
	if pts, _, _ := u.GetChannelPts(ctx, 1000, 4); pts == 401 {
		t.Fatal("broadcast pts written while off")
	}
}
