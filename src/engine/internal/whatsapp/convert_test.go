package whatsapp

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

var (
	ana   = types.NewJID("351911111111", types.DefaultUserServer)
	group = types.NewJID("120363000000000000", types.GroupServer)
)

func evt(chat, sender types.JID, id string, m *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsGroup: chat.Server == types.GroupServer},
			ID:            id, PushName: "Ana", Timestamp: t0,
		},
		Message: m,
	}
}

func convert(t *testing.T, m *waE2E.Message) Row {
	t.Helper()
	r, edit, ok := FromMessage(me, evt(ana, ana, "M1", m), nil)
	if !ok || edit != nil {
		t.Fatalf("ok=%t edit=%v", ok, edit)
	}
	return r
}

func TestPlainText(t *testing.T) {
	if r := convert(t, &waE2E.Message{Conversation: proto.String("hi")}); r.Kind != "text" || r.Text != "hi" ||
		r.ChatJID != ana.String() || r.SenderName != "Ana" || r.ID != "M1" || !r.SentAt.Equal(t0) || r.Account != me {
		t.Fatalf("%+v", r)
	}
	r := convert(t, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("with a link")}})
	if r.Kind != "text" || r.Text != "with a link" {
		t.Fatalf("%+v", r)
	}
}

func TestMediaKindsAndCaptions(t *testing.T) {
	cases := []struct {
		m                    *waE2E.Message
		kind, text, fileName string
	}{
		{&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("beach")}}, "image", "beach", ""},
		{&waE2E.Message{VideoMessage: &waE2E.VideoMessage{}}, "video", "", ""},
		{&waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true)}}, "voice", "", ""},
		{&waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, "audio", "", ""},
		{&waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("bill.pdf"), Caption: proto.String("March")}}, "document", "March", "bill.pdf"},
		{&waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, "sticker", "", ""},
		{&waE2E.Message{LocationMessage: &waE2E.LocationMessage{Name: proto.String("Home")}}, "location", "Home", ""},
		{&waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Bob")}}, "contact", "Bob", ""},
	}
	for _, c := range cases {
		if r := convert(t, c.m); r.Kind != c.kind || r.Text != c.text || r.FileName != c.fileName {
			t.Errorf("%v: got %s/%q/%q", c.m, r.Kind, r.Text, r.FileName)
		}
	}
}

func TestGroupSenderName(t *testing.T) {
	r, _, ok := FromMessage(me, evt(group, ana, "G1", &waE2E.Message{Conversation: proto.String("hello all")}), nil)
	if !ok || r.ChatJID != group.String() || r.SenderJID != ana.String() || r.SenderName != "Ana" {
		t.Fatalf("%+v", r)
	}
}

func TestEditAndRevokeUpdateTheRow(t *testing.T) {
	key := &waCommon.MessageKey{ID: proto.String("M1")}
	_, edit, ok := FromMessage(me, evt(ana, ana, "E1", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: key,
		EditedMessage: &waE2E.Message{Conversation: proto.String("fixed")},
	}}), nil)
	if !ok || edit == nil || *edit != (Edit{ChatJID: ana.String(), ID: "M1", Text: "fixed"}) {
		t.Fatalf("edit %v ok=%t", edit, ok)
	}
	_, edit, ok = FromMessage(me, evt(ana, ana, "R1", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: key,
	}}), nil)
	if !ok || edit == nil || edit.Text != "This message was deleted" || edit.ID != "M1" {
		t.Fatalf("revoke %v ok=%t", edit, ok)
	}
}

func TestNothingToStore(t *testing.T) {
	for name, e := range map[string]*events.Message{
		"reaction": evt(ana, ana, "X", &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍")}}),
		"protocol": evt(ana, ana, "X", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_APP_STATE_SYNC_KEY_SHARE.Enum()}}),
		"status":   evt(types.StatusBroadcastJID, ana, "X", &waE2E.Message{Conversation: proto.String("my story")}),
		"empty":    evt(ana, ana, "X", &waE2E.Message{}),
		"nil":      evt(ana, ana, "X", nil),
	} {
		if _, _, ok := FromMessage(me, e, nil); ok {
			t.Errorf("%s: want nothing to store", name)
		}
	}
}

func TestHistorySync(t *testing.T) {
	web := func(chat types.JID, id, text string) *waHistorySync.HistorySyncMsg {
		return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
			Key:     &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), ID: proto.String(id)},
			Message: &waE2E.Message{Conversation: proto.String(text)},
		}}
	}
	h := &events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{
		{ID: proto.String(ana.String()), Messages: []*waHistorySync.HistorySyncMsg{web(ana, "1", "a"), web(ana, "2", "b")}},
		{ID: proto.String(group.String()), Name: proto.String("Family"), Messages: []*waHistorySync.HistorySyncMsg{web(group, "3", "c")}},
	}}}
	parse := func(chat types.JID, m *waWeb.WebMessageInfo) (*events.Message, error) {
		return evt(chat, ana, m.GetKey().GetID(), m.GetMessage()), nil
	}
	rows, names := FromHistory(me, h, parse, nil)
	if len(rows) != 3 || rows[2].ChatJID != group.String() || rows[2].Text != "c" {
		t.Fatalf("rows %+v", rows)
	}
	if names[group.String()] != "Family" {
		t.Fatalf("names %v", names)
	}
}

func TestLIDChatsBecomePhoneNumbers(t *testing.T) {
	lid := types.NewJID("98765432109876", types.HiddenUserServer)
	in := evt(lid, lid, "1", &waE2E.Message{Conversation: proto.String("hi")})
	in.Info.SenderAlt = ana
	if r, _, _ := FromMessage(me, in, nil); r.ChatJID != ana.String() || r.SenderJID != ana.String() {
		t.Fatalf("incoming via SenderAlt: %+v", r)
	}
	out := evt(lid, types.NewJID("351910000000", types.DefaultUserServer), "2", &waE2E.Message{Conversation: proto.String("hey")})
	out.Info.IsFromMe = true
	out.Info.RecipientAlt = ana
	if r, _, _ := FromMessage(me, out, nil); r.ChatJID != ana.String() {
		t.Fatalf("outgoing via RecipientAlt: %+v", r)
	}
	bare := evt(lid, lid, "3", &waE2E.Message{Conversation: proto.String("hi")})
	resolve := func(j types.JID) (types.JID, bool) { return ana, j == lid }
	if r, _, _ := FromMessage(me, bare, resolve); r.ChatJID != ana.String() {
		t.Fatalf("via the LID store: %+v", r)
	}
	if r, _, _ := FromMessage(me, bare, nil); r.ChatJID != lid.String() {
		t.Fatalf("unknown LID kept as is: %+v", r)
	}
}

// Everything with something to show is stored; a kind this code has no name for becomes "other".
func TestEveryContentKindStored(t *testing.T) {
	cases := []struct {
		m          *waE2E.Message
		kind, text string
	}{
		{&waE2E.Message{PtvMessage: &waE2E.VideoMessage{}}, "video", ""},
		{&waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Dinner?")}}, "other", "Poll: Dinner?"},
		{&waE2E.Message{ListResponseMessage: &waE2E.ListResponseMessage{Title: proto.String("Option B")}}, "text", "Option B"},
		{&waE2E.Message{ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{Response: &waE2E.ButtonsResponseMessage_SelectedDisplayText{SelectedDisplayText: "Yes"}}}, "text", "Yes"},
		{&waE2E.Message{TemplateButtonReplyMessage: &waE2E.TemplateButtonReplyMessage{SelectedDisplayText: proto.String("Track order")}}, "text", "Track order"},
		{&waE2E.Message{GroupInviteMessage: &waE2E.GroupInviteMessage{GroupName: proto.String("Club")}}, "other", ""},
	}
	for _, c := range cases {
		r, _, ok := FromMessage(me, evt(ana, ana, "K", c.m), nil)
		if !ok || r.Kind != c.kind || r.Text != c.text {
			t.Errorf("%v: ok=%t %s/%q", c.m, ok, r.Kind, r.Text)
		}
	}
}
