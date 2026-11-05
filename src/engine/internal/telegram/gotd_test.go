package telegram

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gotd/td/telegram/auth"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type invokeFunc func(ctx context.Context, in bin.Encoder, out bin.Decoder) error

func (f invokeFunc) Invoke(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
	return f(ctx, in, out)
}

func TestFloodWaitRetriesAfterTheWait(t *testing.T) {
	calls := 0
	next := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		if calls == 1 {
			return tgerr.New(420, "FLOOD_WAIT_0")
		}
		return nil
	})
	if err := floodWait(next).Invoke(ctx, nil, nil); err != nil || calls != 2 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}

func TestFloodWaitGivesUpWithTheContext(t *testing.T) {
	next := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return tgerr.New(420, "FLOOD_WAIT_3600") })
	c, cancel := context.WithCancel(ctx)
	cancel()
	if err := floodWait(next).Invoke(c, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestOtherErrorsPassThrough(t *testing.T) {
	calls := 0
	next := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		return tgerr.New(400, "PEER_ID_INVALID")
	})
	if err := floodWait(next).Invoke(ctx, nil, nil); !tgerr.Is(err, "PEER_ID_INVALID") || calls != 1 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}

func TestInputPeer(t *testing.T) {
	if p, ok := inputPeer(InputPeer{Key: "user:1", AccessHash: 11}).(*tg.InputPeerUser); !ok || p.UserID != 1 || p.AccessHash != 11 {
		t.Fatalf("user %+v", p)
	}
	if p, ok := inputPeer(InputPeer{Key: "chat:2"}).(*tg.InputPeerChat); !ok || p.ChatID != 2 {
		t.Fatalf("chat %+v", p)
	}
	if p, ok := inputPeer(InputPeer{Key: "channel:3", AccessHash: 33}).(*tg.InputPeerChannel); !ok || p.ChannelID != 3 || p.AccessHash != 33 {
		t.Fatalf("channel %+v", p)
	}
}

func TestSelfFromUser(t *testing.T) {
	if s := selfFrom(&tg.User{ID: 1000, Phone: "351910000000", FirstName: "Box"}); s != (Self{ID: 1000, Phone: "+351910000000", Name: "Box"}) {
		t.Fatalf("%+v", s)
	}
}

func TestEntitiesFromLists(t *testing.T) {
	ent := entitiesFrom(
		[]tg.UserClass{&tg.User{ID: 1, FirstName: "Ana"}, &tg.UserEmpty{ID: 9}},
		[]tg.ChatClass{&tg.Chat{ID: 2, Title: "Family"}, &tg.Channel{ID: 3, Title: "News", Broadcast: true}, &tg.ChatForbidden{ID: 8}},
	)
	if ent.Users[1].FirstName != "Ana" || ent.Chats[2].Title != "Family" || !ent.Channels[3].Broadcast || len(ent.Users) != 1 || len(ent.Chats) != 1 {
		t.Fatalf("%+v", ent)
	}
}

func TestSessionGoneWhenLinkedButSignedOut(t *testing.T) {
	if err := sessionGone(false, true); !auth.IsUnauthorized(err) {
		t.Fatalf("linked, signed out: %v", err)
	}
	for _, c := range [][2]bool{{true, true}, {false, false}, {true, false}} {
		if err := sessionGone(c[0], c[1]); err != nil {
			t.Fatalf("authorized=%v linked=%v: %v", c[0], c[1], err)
		}
	}
}

func TestA401WhileLinkedEndsTheRun(t *testing.T) {
	revoked := tgerr.New(401, "SESSION_REVOKED")
	next := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return revoked })
	for _, linked := range []bool{true, false} {
		var ended error
		mw := endOn401(func() bool { return linked }, func(err error) { ended = err })
		if err := mw(next).Invoke(ctx, nil, nil); err != revoked {
			t.Fatalf("err %v", err)
		}
		if (ended != nil) != linked {
			t.Fatalf("linked=%v: ended=%v", linked, ended)
		}
	}
	var ended error
	other := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return tgerr.New(400, "PEER_ID_INVALID") })
	endOn401(func() bool { return true }, func(err error) { ended = err })(other).Invoke(ctx, nil, nil)
	if ended != nil {
		t.Fatal("a 400 ended the run")
	}
}

// dialogsAPI answers messages.getDialogs with one chat per folder: the main list and the Archive.
func dialogsAPI(folders *[]int) *tg.Client {
	return tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req := in.(*tg.MessagesGetDialogsRequest)
		*folders = append(*folders, req.FolderID)
		id := int64(100 + req.FolderID)
		out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{
			Dialogs:  []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: id}, Pts: 5}},
			Messages: []tg.MessageClass{&tg.Message{ID: 1, PeerID: &tg.PeerChannel{ChannelID: id}, Date: 1}},
			Chats:    []tg.ChatClass{&tg.Channel{ID: id, AccessHash: 7, Title: "Group", Megagroup: true}},
		}
		return nil
	}))
}

func TestDialogsIncludeTheArchiveFolder(t *testing.T) {
	var folders []int
	var keys []string
	err := dialogsOf(ctx, dialogsAPI(&folders), func(d Dialog) error { keys = append(keys, d.Peer.Key); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(keys, "channel:100") || !slices.Contains(keys, "channel:101") {
		t.Fatalf("dialogs %v (folders asked %v)", keys, folders)
	}
}

// Only a private chat read elsewhere reaches the service; a group's read state is not tracked.
func TestReadInboxEventsAreForPrivateChatsOnly(t *testing.T) {
	d := tg.NewUpdateDispatcher()
	var got []any
	dispatch(d, func(e any) { got = append(got, e) })
	for _, p := range []tg.PeerClass{&tg.PeerUser{UserID: 1}, &tg.PeerChat{ChatID: 2}, &tg.PeerChannel{ChannelID: 3}} {
		u := &tg.UpdateReadHistoryInbox{Peer: p, MaxID: 42}
		if err := d.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{u}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || got[0] != (ReadInbox{Peer: "user:1", MaxID: 42}) {
		t.Fatalf("events %v", got)
	}
}

// Telegram lists the box under Devices by what it sends on connecting, so it names itself and its
// release — never gotd's defaults (gotd's own version, the Go version as the model).
func TestTheBoxNamesItselfToTelegram(t *testing.T) {
	d := NewGotd(nil, nil, "0.1.39").options(nil, nil).Device
	if d.DeviceModel != "Concierge" || d.SystemVersion != "Linux" || d.AppVersion != "0.1.39" {
		t.Fatalf("%+v", d)
	}
}
