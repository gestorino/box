package whatsapp

import (
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Edit changes a stored message: a new text after an edit, or the deletion notice.
type Edit struct{ ChatJID, ID, Text string }

const deletedText = "This message was deleted"

// FromMessage turns a WhatsApp message event into a row to store, or an edit to an existing one.
// ok is false for anything with nothing to show: reactions, protocol housekeeping, status updates.
// whatsmeow has already unwrapped ephemeral, view-once and edited-message wrappers. Chats and senders
// are stored under their phone number: WhatsApp addresses many DMs by LID now, and the same person
// must not become two chats (pnOf resolves a LID with no phone number alongside; it may be nil).
func FromMessage(account string, e *events.Message, pnOf func(types.JID) (types.JID, bool)) (row Row, edit *Edit, ok bool) {
	chat := e.Info.Chat.ToNonAD()
	if e.Message == nil || chat == types.StatusBroadcastJID || chat.Server == types.NewsletterServer {
		return Row{}, nil, false
	}
	if !e.Info.IsGroup {
		alt := e.Info.SenderAlt
		if e.Info.IsFromMe {
			alt = e.Info.RecipientAlt
		}
		chat = phoneNumber(chat, alt, pnOf)
	}
	if pm := e.Message.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			return Row{}, &Edit{ChatJID: chat.String(), ID: pm.GetKey().GetID(), Text: deletedText}, true
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			_, text, _ := content(pm.GetEditedMessage())
			return Row{}, &Edit{ChatJID: chat.String(), ID: pm.GetKey().GetID(), Text: text}, true
		}
		return Row{}, nil, false
	}
	kind, text, fileName := content(e.Message)
	if kind == "" {
		return Row{}, nil, false
	}
	sender := phoneNumber(e.Info.Sender.ToNonAD(), e.Info.SenderAlt, pnOf)
	return Row{
		Account: account, ChatJID: chat.String(), ID: e.Info.ID,
		SenderJID: sender.String(), SenderName: e.Info.PushName, FromMe: e.Info.IsFromMe,
		SentAt: e.Info.Timestamp, Kind: kind, Text: text, FileName: fileName,
	}, nil, true
}

// phoneNumber is jid's phone-number form when it is a LID and one is known; otherwise jid.
func phoneNumber(jid, alt types.JID, pnOf func(types.JID) (types.JID, bool)) types.JID {
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	if alt.Server == types.DefaultUserServer {
		return alt.ToNonAD()
	}
	if pnOf != nil {
		if pn, ok := pnOf(jid); ok {
			return pn.ToNonAD()
		}
	}
	return jid
}

// content names what a message is and the text to show for it (body or caption). kind is "" when
// there is nothing to show.
func content(m *waE2E.Message) (kind, text, fileName string) {
	switch {
	case m == nil:
		return "", "", ""
	case m.Conversation != nil:
		return "text", m.GetConversation(), ""
	case m.ExtendedTextMessage != nil:
		return "text", m.GetExtendedTextMessage().GetText(), ""
	case m.ImageMessage != nil:
		return "image", m.GetImageMessage().GetCaption(), ""
	case m.VideoMessage != nil:
		return "video", m.GetVideoMessage().GetCaption(), ""
	case m.AudioMessage != nil:
		if m.GetAudioMessage().GetPTT() {
			return "voice", "", ""
		}
		return "audio", "", ""
	case m.DocumentMessage != nil:
		d := m.GetDocumentMessage()
		return "document", d.GetCaption(), d.GetFileName()
	case m.StickerMessage != nil:
		return "sticker", "", ""
	case m.LocationMessage != nil:
		l := m.GetLocationMessage()
		if l.GetName() != "" {
			return "location", l.GetName(), ""
		}
		return "location", l.GetAddress(), ""
	case m.LiveLocationMessage != nil:
		return "location", m.GetLiveLocationMessage().GetCaption(), ""
	case m.ContactMessage != nil:
		return "contact", m.GetContactMessage().GetDisplayName(), ""
	case m.ContactsArrayMessage != nil:
		return "contact", m.GetContactsArrayMessage().GetDisplayName(), ""
	case m.PtvMessage != nil:
		return "video", m.GetPtvMessage().GetCaption(), ""
	case m.ListResponseMessage != nil:
		return "text", m.GetListResponseMessage().GetTitle(), ""
	case m.ButtonsResponseMessage != nil:
		return "text", m.GetButtonsResponseMessage().GetSelectedDisplayText(), ""
	case m.TemplateButtonReplyMessage != nil:
		return "text", m.GetTemplateButtonReplyMessage().GetSelectedDisplayText(), ""
	case m.InteractiveResponseMessage != nil:
		return "text", m.GetInteractiveResponseMessage().GetBody().GetText(), ""
	}
	for _, p := range []*waE2E.PollCreationMessage{m.PollCreationMessage, m.PollCreationMessageV2,
		m.PollCreationMessageV3, m.PollCreationMessageV5, m.PollCreationMessageV6} {
		if p != nil {
			return "other", "Poll: " + p.GetName(), ""
		}
	}
	if fp := m.PollCreationMessageV4; fp != nil { // a FutureProofMessage around the poll
		if kind, text, _ := content(fp.GetMessage()); kind != "" {
			return kind, text, ""
		}
		return "other", "Poll", ""
	}
	// Anything else with content is still a message: stored as "other" rather than dropped.
	other := false
	m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if !housekeeping[string(fd.Name())] {
			other = true
			return false
		}
		return true
	})
	if other {
		return "other", "", ""
	}
	return "", "", ""
}

// housekeeping are the Message fields that carry nothing to show on their own.
var housekeeping = map[string]bool{
	"protocolMessage": true, "reactionMessage": true, "encReactionMessage": true, "pollUpdateMessage": true,
	"keepInChatMessage": true, "pinInChatMessage": true, "stickerSyncRmrMessage": true,
	"placeholderMessage": true, "senderKeyDistributionMessage": true, "messageContextInfo": true,
	"fastRatchetKeySenderKeyDistributionMessage": true, "call": true, "encEventResponseMessage": true,
	"secretEncryptedMessage": true, "messageHistoryBundle": true, "messageHistoryNotice": true,
}

// FromHistory turns the history WhatsApp sends a newly linked device into rows, plus the chat names
// it carries. parse is the client's ParseWebMessage; pnOf as for FromMessage.
func FromHistory(account string, h *events.HistorySync, parse func(types.JID, *waWeb.WebMessageInfo) (*events.Message, error),
	pnOf func(types.JID) (types.JID, bool)) ([]Row, map[string]string) {
	var rows []Row
	names := map[string]string{}
	for _, conv := range h.Data.GetConversations() {
		chat, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		if pn, err := types.ParseJID(conv.GetPnJID()); chat.Server == types.HiddenUserServer && err == nil && pn.Server == types.DefaultUserServer {
			chat = pn
		}
		if conv.GetName() != "" {
			names[chat.ToNonAD().String()] = conv.GetName()
		}
		for _, hm := range conv.GetMessages() {
			e, err := parse(chat, hm.GetMessage())
			if err != nil {
				continue
			}
			if r, edit, ok := FromMessage(account, e, pnOf); ok && edit == nil {
				rows = append(rows, r)
			}
		}
	}
	return rows, names
}
