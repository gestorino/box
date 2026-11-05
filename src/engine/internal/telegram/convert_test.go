package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

var ents = tg.Entities{
	Users: map[int64]*tg.User{
		1:    {ID: 1, FirstName: "Ana", LastName: "Silva", AccessHash: 11},
		5:    {ID: 5, Username: "bob"},
		6:    {ID: 6, Phone: "351920000000"},
		1000: {ID: 1000, FirstName: "Box", Self: true},
	},
	Chats:    map[int64]*tg.Chat{2: {ID: 2, Title: "Family"}},
	Channels: map[int64]*tg.Channel{3: {ID: 3, Title: "Neighbours", Megagroup: true, AccessHash: 33}, 4: {ID: 4, Title: "News", Broadcast: true, AccessHash: 44}},
}

func msg(peer tg.PeerClass, id int, text string) *tg.Message {
	return &tg.Message{ID: id, PeerID: peer, Message: text, Date: int(t0.Unix())}
}

func TestPeerKeyAndPeerFrom(t *testing.T) {
	for key, want := range map[string]Peer{
		"user:1":    {Account: me, Key: "user:1", AccessHash: 11, Name: "Ana Silva", Kind: "private"},
		"user:5":    {Account: me, Key: "user:5", Name: "@bob", Username: "bob", Kind: "private"},
		"user:6":    {Account: me, Key: "user:6", Name: "+351920000000", Kind: "private"},
		"chat:2":    {Account: me, Key: "chat:2", Name: "Family", Kind: "group"},
		"channel:3": {Account: me, Key: "channel:3", AccessHash: 33, Name: "Neighbours", Kind: "group"},
		"channel:4": {Account: me, Key: "channel:4", AccessHash: 44, Name: "News", Kind: "channel"},
	} {
		if got, ok := PeerFrom(me, key, ents); !ok || got != want {
			t.Errorf("%s: %+v, want %+v", key, got, want)
		}
	}
	if _, ok := PeerFrom(me, "user:404", ents); ok {
		t.Error("unknown user found")
	}
	if k := PeerKey(&tg.PeerChannel{ChannelID: 3}); k != "channel:3" {
		t.Errorf("key %q", k)
	}
}

func TestFromMessageText(t *testing.T) {
	r, ok := FromMessage(me, msg(&tg.PeerUser{UserID: 1}, 7, "hi"), ents)
	want := Row{Account: me, Peer: "user:1", ID: 7, Sender: "user:1", SenderName: "Ana Silva", SentAt: t0, Kind: "text", Text: "hi"}
	if !ok || r != want {
		t.Fatalf("%+v", r)
	}
}

func TestFromMessageOutIsFromMe(t *testing.T) {
	m := msg(&tg.PeerUser{UserID: 1}, 8, "sent from the phone")
	m.Out = true
	r, _ := FromMessage(me, m, ents)
	if !r.FromMe || r.Sender != "user:1000" || r.SenderName != "Box" {
		t.Fatalf("%+v", r)
	}
}

func TestFromMessageGroupSender(t *testing.T) {
	m := msg(&tg.PeerChannel{ChannelID: 3}, 9, "hello all")
	m.FromID = &tg.PeerUser{UserID: 5}
	r, _ := FromMessage(me, m, ents)
	if r.Peer != "channel:3" || r.Sender != "user:5" || r.SenderName != "@bob" {
		t.Fatalf("%+v", r)
	}
}

func TestFromMessageChannelPost(t *testing.T) {
	r, _ := FromMessage(me, msg(&tg.PeerChannel{ChannelID: 4}, 10, "breaking"), ents)
	if r.Sender != "channel:4" || r.SenderName != "News" {
		t.Fatalf("%+v", r)
	}
}

func doc(attrs ...tg.DocumentAttributeClass) *tg.MessageMediaDocument {
	return &tg.MessageMediaDocument{Document: &tg.Document{Attributes: attrs}}
}

func TestFromMessageMedia(t *testing.T) {
	for _, c := range []struct {
		media      tg.MessageMediaClass
		kind, file string
	}{
		{&tg.MessageMediaPhoto{}, "photo", ""},
		{doc(&tg.DocumentAttributeAudio{Voice: true}), "voice", ""},
		{doc(&tg.DocumentAttributeAudio{Title: "Song"}, &tg.DocumentAttributeFilename{FileName: "song.mp3"}), "audio", ""},
		{doc(&tg.DocumentAttributeVideo{}), "video", ""},
		{doc(&tg.DocumentAttributeSticker{}), "sticker", ""},
		{doc(&tg.DocumentAttributeFilename{FileName: "report.pdf"}), "document", "report.pdf"},
		{&tg.MessageMediaGeo{}, "location", ""},
		{&tg.MessageMediaVenue{}, "location", ""},
		{&tg.MessageMediaGeoLive{}, "location", ""},
		{&tg.MessageMediaContact{}, "contact", ""},
		{&tg.MessageMediaDice{}, "other", ""},
	} {
		m := msg(&tg.PeerUser{UserID: 1}, 11, "caption")
		m.Media = c.media
		r, _ := FromMessage(me, m, ents)
		if r.Kind != c.kind || r.FileName != c.file || r.Text != "caption" {
			t.Errorf("%T: %+v", c.media, r)
		}
	}
}

func TestFromMessagePollTextIsQuestion(t *testing.T) {
	m := msg(&tg.PeerChat{ChatID: 2}, 12, "")
	m.Media = &tg.MessageMediaPoll{Poll: tg.Poll{Question: tg.TextWithEntities{Text: "Dinner at 8?"}}}
	m.FromID = &tg.PeerUser{UserID: 1}
	r, _ := FromMessage(me, m, ents)
	if r.Kind != "poll" || r.Text != "Dinner at 8?" || r.Peer != "chat:2" {
		t.Fatalf("%+v", r)
	}
}

func TestFromMessageSkipsServiceAndEmpty(t *testing.T) {
	for _, m := range []tg.MessageClass{
		&tg.MessageService{ID: 1, PeerID: &tg.PeerChat{ChatID: 2}},
		&tg.MessageEmpty{ID: 2},
	} {
		if _, ok := FromMessage(me, m, ents); ok {
			t.Errorf("%T stored", m)
		}
	}
}
