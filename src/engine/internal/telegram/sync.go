package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gotd/td/tg"
)

// backfillDepth is how many of each chat's newest messages the box copies when it links.
const backfillDepth = 100

func (s *Service) Chats(ctx context.Context) ([]Chat, error) {
	return s.store.Chats(ctx, s.account(), s.selfKey())
}

var (
	errUnknownChat = errors.New("unknown chat")
	errBadSend     = errors.New(`body must be {"peer": a chat's key or "self", "text": 1–4096 characters}`)
)

const maxText = 4096

// Send sends text to a chat the archive holds, or "self" (Saved Messages), and stores it as the
// owner's: what this session sends does not come back as an update. Sending may show the owner
// online for a moment; the status is set offline again right after.
func (s *Service) Send(ctx context.Context, peer, text string) error {
	if n := utf8.RuneCountInString(text); peer == "" || n < 1 || n > maxText {
		return errBadSend
	}
	s.mu.Lock()
	state, self := s.state, s.self
	s.mu.Unlock()
	if state != "connected" || self.ID == 0 {
		return ErrNotConnected
	}
	account, to, key := acct(self.ID), InputPeer{Key: "self"}, fmt.Sprintf("user:%d", self.ID)
	if peer != "self" && peer != key {
		p, ok, err := s.store.Peer(ctx, account, peer)
		if err != nil {
			return err
		}
		if !ok {
			return errUnknownChat
		}
		to, key = InputPeer{Key: p.Key, AccessHash: p.AccessHash}, p.Key
	}
	id, at, err := s.c.Send(ctx, to, text)
	if err != nil {
		return err
	}
	if err := s.c.Offline(ctx); err != nil {
		slog.Warn("telegram: offline status after a send", "err", err)
	}
	return s.store.Save(ctx, Row{Account: account, Peer: key, ID: id, Sender: "user:" + account,
		SenderName: self.Name, FromMe: true, SentAt: at, Kind: "text", Text: text})
}

// SelfChat is Saved Messages' key in the archive, while connected.
func (s *Service) SelfChat() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("user:%d", s.self.ID), s.state == "connected" && s.self.ID != 0
}

func (s *Service) Messages(ctx context.Context, peer string) ([]Msg, error) {
	return s.store.Messages(ctx, s.account(), peer, messagesShown)
}

// SetArchiveChannels turns archiving broadcast channels on (backfilling them and tracking their
// updates) or off (no longer tracked; what was stored stays).
func (s *Service) SetArchiveChannels(ctx context.Context, on bool) error {
	v := ""
	if on {
		v = "1"
	}
	if err := s.store.Set(ctx, "archive_channels", v); err != nil {
		return err
	}
	s.mu.Lock()
	s.archiveChannels = on
	s.mu.Unlock()
	s.us.TrackBroadcasts(on)
	account := s.account()
	if account == "" {
		return nil
	}
	if on {
		s.background(func(ctx context.Context) { s.backfillLogged(ctx, isBroadcast) })
		return nil
	}
	ids, err := s.store.Broadcasts(ctx, account)
	if err != nil {
		return err
	}
	if err := s.us.DropChannels(ctx, account, ids); err != nil {
		return err
	}
	// gotd keeps polling the channels it already tracks in memory until its run ends.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creds != nil {
		s.startRun()
	}
	return nil
}

func isBroadcast(p Peer) bool { return p.Kind == "channel" }

func isCommon(p Peer) bool { return !strings.HasPrefix(p.Key, "channel:") }

// background runs f until the service stops.
func (s *Service) background(f func(ctx context.Context)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		f(s.life)
	}()
}

// backfill copies the newest messages of every dialog `which` selects, and seeds the update state
// of each supergroup and channel: Telegram's getDifference covers only private chats and basic
// groups, and gotd recovers a channel's gap only from a pts it knows.
// It fails when any chat could not be copied; the rest are still copied.
func (s *Service) backfill(ctx context.Context, which func(Peer) bool) error {
	s.mu.Lock()
	self, channels := s.self, s.archiveChannels
	s.mu.Unlock()
	if self.ID == 0 {
		return errNotLinked
	}
	account := acct(self.ID)
	failed := 0
	err := s.c.Dialogs(ctx, func(d Dialog) error {
		p := d.Peer
		p.Account = account
		if !which(p) || (isBroadcast(p) && !channels) {
			return nil
		}
		if err := s.store.PutPeer(ctx, p); err != nil {
			return err
		}
		if kind, id := splitKey(p.Key); kind == "channel" {
			if err := s.us.SetChannelPts(ctx, self.ID, id, d.Pts); err != nil {
				return err
			}
		}
		if err := s.copyChat(ctx, account, p); err != nil {
			slog.Warn("telegram: backfill history", "peer", p.Key, "err", err)
			failed++
		}
		return nil // the next chat still gets its copy
	})
	if err == nil && failed > 0 {
		err = fmt.Errorf("%d chats not copied", failed)
	}
	return err
}

var errNotLinked = errors.New("not linked")

// copyChat stores one chat's newest messages.
func (s *Service) copyChat(ctx context.Context, account string, p Peer) error {
	msgs, ent, err := s.c.History(ctx, InputPeer{Key: p.Key, AccessHash: p.AccessHash}, backfillDepth)
	if err != nil {
		return err
	}
	s.peers(ctx, account, ent)
	for _, m := range msgs {
		if r, ok := FromMessage(account, m, ent); ok {
			if err := s.store.Save(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyLater copies one chat in the background.
func (s *Service) copyLater(account string, p Peer) {
	s.background(func(ctx context.Context) {
		if err := s.copyChat(ctx, account, p); err != nil && ctx.Err() == nil {
			slog.Warn("telegram: copying a chat", "peer", p.Key, "err", err)
		}
	})
}

func (s *Service) backfillLogged(ctx context.Context, which func(Peer) bool) {
	if err := s.backfill(ctx, which); err != nil && ctx.Err() == nil {
		slog.Error("telegram: backfill", "err", err)
	}
}

func (s *Service) peers(ctx context.Context, account string, ent tg.Entities) {
	for _, p := range Peers(account, ent) {
		if err := s.store.PutPeer(ctx, p); err != nil {
			slog.Error("telegram: storing a peer", "peer", p.Key, "err", err)
		}
	}
}

// firstBackfill runs after each connect until one complete copy of the link succeeds; a reconnect
// while it runs does not start a second.
func (s *Service) firstBackfill(ctx context.Context) {
	if !s.backfilling.TryLock() {
		return
	}
	defer s.backfilling.Unlock()
	if v, _ := s.store.Get(ctx, "backfilled"); v == "1" {
		return
	}
	if err := s.backfill(ctx, func(Peer) bool { return true }); err != nil {
		if ctx.Err() == nil {
			slog.Warn("telegram: backfill incomplete, retried on the next connect", "err", err)
		}
		return
	}
	if err := s.store.Set(ctx, "backfilled", "1"); err != nil {
		slog.Error("telegram: marking backfilled", "err", err)
	}
}

// message stores a new or edited message, dropping broadcast posts while the switch is off.
func (s *Service) message(ctx context.Context, m tg.MessageClass, ent tg.Entities, edit bool) {
	account := s.account()
	if account == "" {
		return
	}
	r, ok := FromMessage(account, m, ent)
	if !ok {
		s.peers(ctx, account, ent)
		return
	}
	_, known, _ := s.store.Peer(ctx, account, r.Peer)
	s.peers(ctx, account, ent)
	s.mu.Lock()
	channels := s.archiveChannels
	s.mu.Unlock()
	p, _, _ := s.store.Peer(ctx, account, r.Peer)
	if isBroadcast(p) && !channels {
		return
	}
	// A group the box first meets after the link's backfill (joined since) gets the same copy.
	if !known && !edit && p.Kind != "private" {
		if v, _ := s.store.Get(ctx, "backfilled"); v == "1" {
			s.copyLater(account, p)
		}
	}
	var err error
	if edit {
		err = s.store.Edit(ctx, r)
	} else {
		err = s.store.Save(ctx, r)
	}
	if err != nil {
		slog.Error("telegram: storing a message", "peer", r.Peer, "id", r.ID, "err", err)
	}
}

// tooLong: Telegram will not fill a gap; copy the newest messages again (the archive may keep a
// hole between those and what it had).
func (s *Service) tooLong(channelID int64) {
	if channelID == 0 {
		s.background(func(ctx context.Context) { s.backfillLogged(ctx, isCommon) })
		return
	}
	account := s.account()
	if account == "" {
		return
	}
	key := "channel:" + strconv.FormatInt(channelID, 10)
	if p, ok, _ := s.store.Peer(s.life, account, key); ok && p.AccessHash != 0 {
		s.copyLater(account, p)
		return
	}
	s.background(func(ctx context.Context) { s.backfillLogged(ctx, func(p Peer) bool { return p.Key == key }) })
}
