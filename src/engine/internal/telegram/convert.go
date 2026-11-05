package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// PeerKey is how a peer is stored: user:<id>, chat:<id> (a basic group) or channel:<id> (a
// supergroup or a broadcast channel).
func PeerKey(p tg.PeerClass) string {
	switch p := p.(type) {
	case *tg.PeerUser:
		return fmt.Sprintf("user:%d", p.UserID)
	case *tg.PeerChat:
		return fmt.Sprintf("chat:%d", p.ChatID)
	case *tg.PeerChannel:
		return fmt.Sprintf("channel:%d", p.ChannelID)
	}
	return ""
}

func splitKey(key string) (string, int64) {
	kind, id, _ := strings.Cut(key, ":")
	n, _ := strconv.ParseInt(id, 10, 64)
	return kind, n
}

// PeerFrom is what the entities Telegram sent say about a peer.
func PeerFrom(account, key string, ent tg.Entities) (Peer, bool) {
	kind, id := splitKey(key)
	p := Peer{Account: account, Key: key}
	switch kind {
	case "user":
		u, ok := ent.Users[id]
		if !ok {
			return Peer{}, false
		}
		p.AccessHash, p.Username, p.Kind = u.AccessHash, u.Username, "private"
		p.Name = userName(u)
	case "chat":
		c, ok := ent.Chats[id]
		if !ok {
			return Peer{}, false
		}
		p.Name, p.Kind = c.Title, "group"
	case "channel":
		c, ok := ent.Channels[id]
		if !ok {
			return Peer{}, false
		}
		p.AccessHash, p.Name, p.Username, p.Kind = c.AccessHash, c.Title, c.Username, "group"
		if c.Broadcast {
			p.Kind = "channel"
		}
	default:
		return Peer{}, false
	}
	return p, true
}

func userName(u *tg.User) string {
	switch name := strings.TrimSpace(u.FirstName + " " + u.LastName); {
	case name != "":
		return name
	case u.Username != "":
		return "@" + u.Username
	case u.Phone != "":
		return "+" + u.Phone
	}
	return ""
}

// Peers are every peer the entities describe.
func Peers(account string, ent tg.Entities) []Peer {
	var out []Peer
	add := func(key string) {
		if p, ok := PeerFrom(account, key, ent); ok {
			out = append(out, p)
		}
	}
	for id := range ent.Users {
		add(fmt.Sprintf("user:%d", id))
	}
	for id := range ent.Chats {
		add(fmt.Sprintf("chat:%d", id))
	}
	for id := range ent.Channels {
		add(fmt.Sprintf("channel:%d", id))
	}
	return out
}

// FromMessage maps a message to a row; service messages (joins, title changes, pins) and empty
// ones are not stored.
func FromMessage(account string, mc tg.MessageClass, ent tg.Entities) (Row, bool) {
	m, ok := mc.(*tg.Message)
	if !ok {
		return Row{}, false
	}
	r := Row{Account: account, Peer: PeerKey(m.PeerID), ID: m.ID, FromMe: m.Out,
		SentAt: time.Unix(int64(m.Date), 0).UTC(), Kind: "text", Text: m.Message}
	switch {
	case m.Out:
		r.Sender = "user:" + account
	case m.FromID != nil:
		r.Sender = PeerKey(m.FromID)
	default: // a private chat's other side, or a channel post
		r.Sender = r.Peer
	}
	if p, ok := PeerFrom(account, r.Sender, ent); ok {
		r.SenderName = p.Name
	}
	if m.Media != nil {
		r.Kind, r.FileName = media(m.Media)
		if poll, ok := m.Media.(*tg.MessageMediaPoll); ok {
			r.Text = poll.Poll.Question.Text
		}
	}
	return r, true
}

func media(mm tg.MessageMediaClass) (kind, fileName string) {
	switch mm := mm.(type) {
	case *tg.MessageMediaPhoto:
		return "photo", ""
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return "location", ""
	case *tg.MessageMediaContact:
		return "contact", ""
	case *tg.MessageMediaPoll:
		return "poll", ""
	case *tg.MessageMediaDocument:
		d, ok := mm.Document.(*tg.Document)
		if !ok {
			return "document", ""
		}
		kind = "document"
		for _, a := range d.Attributes {
			switch a := a.(type) {
			case *tg.DocumentAttributeAudio:
				kind = "audio"
				if a.Voice {
					return "voice", ""
				}
			case *tg.DocumentAttributeVideo:
				return "video", ""
			case *tg.DocumentAttributeSticker:
				return "sticker", ""
			case *tg.DocumentAttributeFilename:
				fileName = a.FileName
			}
		}
		if kind != "document" {
			return kind, ""
		}
		return kind, fileName
	}
	return "other", ""
}
