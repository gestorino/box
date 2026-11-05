package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Client is everything the service asks of whatsmeow; meow.go implements it, tests fake it.
// QRChannel must be called before Connect, as whatsmeow requires.
type Client interface {
	Connect() error
	Disconnect()
	Logout(ctx context.Context) error // tells WhatsApp, then forgets the session
	Forget(ctx context.Context) error // forgets the local session (WhatsApp already removed the box)
	QRChannel(ctx context.Context) (<-chan QRItem, error)
	OnEvent(func(any))
	Account() string // the linked account's JID, "" when not linked
	PNForLID(ctx context.Context, lid types.JID) (types.JID, bool)
	PushName() string
	GroupName(ctx context.Context, jid string) (string, error)
	PresenceUnavailable(ctx context.Context) error
	// SendText sends a text message to chat as the linked device; id and at are WhatsApp's.
	SendText(ctx context.Context, chat types.JID, text string) (id string, at time.Time, err error)
	ParseWebMessage(chat types.JID, m *waWeb.WebMessageInfo) (*events.Message, error)
	// MarkRead tells WhatsApp the owner read these messages of a private chat, as of at.
	MarkRead(ctx context.Context, ids []string, at time.Time, chat types.JID) error
}

// QRItem mirrors whatsmeow's QRChannelItem. Event is "code", "success", "timeout" or "error".
type QRItem struct {
	Event, Code string
	Timeout     time.Duration
}

// Status: State is unlinked, pairing, connecting, connected or logged_out.
type Status struct {
	State string `json:"state"`
	Phone string `json:"phone,omitempty"`
	Name  string `json:"name,omitempty"`
}

var ErrState = errors.New("not possible in this state")

// ErrNotConnected: a send needs the session up.
var ErrNotConnected = errors.New("not connected")

// errBadSend is a send with no text, too long a text, or a chat that is neither a phone number nor a
// person's or group's JID.
var errBadSend = errors.New(`body must be {"chat": a phone number or a chat's jid, "text": 1–4096 characters}`)

var dialable = regexp.MustCompile(`^\+?\d{3,20}$`)

const maxText = 4096

// ErrLogout is a logout that could not reach WhatsApp; the owner may then remove anyway.
var ErrLogout = errors.New("couldn't reach WhatsApp to log out")

const messagesShown = 500

// connectRetry is how long Start waits before trying again when the first connect fails (a box
// that booted before its uplink was up).
const connectRetry = 10 * time.Second

type Service struct {
	c       Client
	archive *Archive
	now     func() time.Time

	retryDelay  time.Duration
	markTimeout time.Duration // one mark sent to WhatsApp

	nudge    chan struct{} // asks flushLoop to flush now
	flushing sync.Mutex    // one flush at a time, so a mark is not sent twice

	mu        sync.Mutex
	state     string
	qrCode    string
	qrExpires time.Time
	named     map[string]bool // groups whose name was looked up this run
}

func NewService(c Client, a *Archive, now func() time.Time) *Service {
	s := &Service{c: c, archive: a, now: now, state: "unlinked", named: map[string]bool{}, retryDelay: connectRetry, markTimeout: markTimeout,
		nudge: make(chan struct{}, 1)}
	c.OnEvent(s.handle)
	return s
}

// Start connects a linked session (no QR code) in the background, retrying until it succeeds, or
// settles on unlinked / logged_out.
func (s *Service) Start(ctx context.Context) error {
	go s.flushLoop(ctx)
	if s.c.Account() != "" {
		s.set("connecting")
		go s.connect()
		return nil
	}
	st, err := s.archive.State(ctx)
	if err != nil {
		return err
	}
	if st == "logged_out" {
		s.set("logged_out")
	} else {
		s.set("unlinked")
	}
	return nil
}

func (s *Service) Status() Status {
	s.mu.Lock()
	st := Status{State: s.state}
	s.mu.Unlock()
	if account := s.c.Account(); account != "" {
		if jid, err := types.ParseJID(account); err == nil {
			st.Phone = "+" + jid.User
		}
		st.Name = s.c.PushName()
	}
	return st
}

// Link starts pairing: a QR code to scan from the phone, rotated by WhatsApp until it is scanned or
// the codes run out.
func (s *Service) Link(ctx context.Context) error {
	s.mu.Lock()
	if s.state != "unlinked" && s.state != "logged_out" {
		s.mu.Unlock()
		return ErrState
	}
	s.state = "pairing"
	s.mu.Unlock()
	ch, err := s.c.QRChannel(context.Background())
	if err == nil {
		err = s.c.Connect()
	}
	if err != nil {
		s.set("unlinked")
		return err
	}
	go s.follow(ch)
	return nil
}

// connect keeps trying until the first connect succeeds; after that whatsmeow reconnects by itself.
func (s *Service) connect() {
	for {
		err := s.c.Connect()
		if err == nil || s.c.Account() == "" {
			return
		}
		slog.Warn("whatsapp: connecting, will retry", "err", err, "in", s.retryDelay)
		time.Sleep(s.retryDelay)
	}
}

func (s *Service) follow(ch <-chan QRItem) {
	for item := range ch {
		s.mu.Lock()
		switch item.Event {
		case "code":
			s.qrCode, s.qrExpires = item.Code, s.now().Add(item.Timeout)
		case "success":
			s.qrCode = ""
			if s.state == "pairing" {
				s.state = "connecting"
			}
		case "timeout", "error", "err-unexpected-state", "err-client-outdated", "err-scanned-without-multidevice":
			s.qrCode = ""
			if s.state == "pairing" {
				s.state = "unlinked"
			}
		}
		// Anything else (passkey-request, passkey-confirmation, …) is a step within pairing, not its end.
		s.mu.Unlock()
	}
}

// QR is the current code to scan while pairing.
func (s *Service) QR() (code string, expiresIn time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "pairing" || s.qrCode == "" {
		return "", 0, false
	}
	return s.qrCode, s.qrExpires.Sub(s.now()), true
}

// Unlink logs the box out of the account; with deleteChats it also deletes that account's archive.
func (s *Service) Unlink(ctx context.Context, deleteChats bool) error {
	account := s.c.Account()
	if account == "" {
		return ErrState
	}
	if err := s.c.Logout(ctx); err != nil {
		return err
	}
	if deleteChats {
		if err := s.archive.DeleteAccount(ctx, account); err != nil {
			return err
		}
	}
	if err := s.archive.SetState(ctx, ""); err != nil {
		return err
	}
	s.set("unlinked")
	return nil
}

// Remove is the owner removing WhatsApp, in any state: log the box out, or — when that cannot reach
// WhatsApp and the owner approved (force) — forget the session locally, leaving the device listed on
// the phone. deleteData wipes every account's chats. Repeating it succeeds.
func (s *Service) Remove(ctx context.Context, deleteData, force bool) error {
	if s.c.Account() != "" {
		if err := s.c.Logout(ctx); err != nil {
			if !force {
				return fmt.Errorf("%w: %v", ErrLogout, err)
			}
			if err := s.c.Forget(ctx); err != nil {
				return err
			}
		}
	}
	s.mu.Lock()
	pairing := s.state == "pairing"
	s.mu.Unlock()
	if pairing {
		s.c.Disconnect() // ends the QR codes
	}
	if deleteData {
		if err := s.archive.Wipe(ctx); err != nil {
			return err
		}
	}
	if err := s.archive.SetState(ctx, ""); err != nil {
		return err
	}
	s.set("unlinked")
	return nil
}

func (s *Service) Chats(ctx context.Context) ([]Chat, error) {
	return s.archive.Chats(ctx, s.c.Account())
}

// Send sends text to chat — a stored chat's JID, or a phone number — and stores it as the owner's,
// since whatsmeow does not echo what this device sends (an echo would be stored once, by its id).
func (s *Service) Send(ctx context.Context, chat, text string) error {
	jid, ok := sendTarget(chat)
	if n := utf8.RuneCountInString(text); !ok || n < 1 || n > maxText {
		return errBadSend
	}
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	account := s.c.Account()
	if state != "connected" || account == "" {
		return ErrNotConnected
	}
	id, at, err := s.c.SendText(ctx, jid, text)
	if err != nil {
		return err
	}
	return s.archive.Save(ctx, Row{Account: account, ChatJID: jid.String(), ID: id, SenderJID: account,
		FromMe: true, SentAt: at, Kind: "text", Text: text})
}

func sendTarget(chat string) (types.JID, bool) {
	if number := strings.Join(strings.Fields(chat), ""); dialable.MatchString(number) {
		return types.NewJID(strings.TrimPrefix(number, "+"), types.DefaultUserServer), true
	}
	jid, err := types.ParseJID(chat)
	if err != nil || jid.User == "" || jid.Server != types.DefaultUserServer && jid.Server != types.GroupServer {
		return types.JID{}, false
	}
	return jid.ToNonAD(), true
}

// SelfChat is the account's own chat ("Message yourself"), while connected.
func (s *Service) SelfChat() (string, bool) {
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	account := s.c.Account()
	return account, state == "connected" && account != ""
}

func (s *Service) Messages(ctx context.Context, chat string) ([]Msg, error) {
	return s.archive.Messages(ctx, s.c.Account(), chat, messagesShown)
}

func (s *Service) set(state string) {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
}

// handle runs on whatsmeow's event goroutine.
func (s *Service) handle(evt any) {
	ctx := context.Background()
	switch e := evt.(type) {
	case *events.PairSuccess:
		s.set("connecting")
		if err := s.archive.SetState(ctx, ""); err != nil {
			slog.Error("whatsapp: clearing logged_out", "err", err)
		}
	case *events.Connected:
		s.set("connected")
		select { // tell WhatsApp what was read while it was down
		case s.nudge <- struct{}{}:
		default:
		}
		go func() { // never shown as online
			if err := s.c.PresenceUnavailable(context.Background()); err != nil {
				slog.Warn("whatsapp: presence", "err", err)
			}
		}()
	case *events.Disconnected:
		// whatsmeow reconnects a linked session by itself. Unlinked, a disconnect is how an
		// unscanned QR code ends; the QR channel's "timeout" that follows settles the state.
		if s.c.Account() != "" {
			s.set("connecting")
		}
	case *events.LoggedOut:
		s.set("logged_out")
		if err := s.archive.SetState(ctx, "logged_out"); err != nil {
			slog.Error("whatsapp: persisting logged_out", "err", err)
		}
		if err := s.c.Forget(ctx); err != nil {
			slog.Error("whatsapp: forgetting the session", "err", err)
		}
	case *events.Message:
		s.message(ctx, e)
	case *events.HistorySync:
		s.history(ctx, e)
	case *events.Receipt:
		// Read on one of the owner's other devices: WhatsApp already knows, so nothing to tell.
		if account := s.c.Account(); account != "" && e.IsFromMe && (e.Type == types.ReceiptTypeRead || e.Type == types.ReceiptTypeReadSelf) {
			chat := phoneNumber(e.Chat.ToNonAD(), e.RecipientAlt, s.pnOf)
			if err := s.archive.ReadIDs(ctx, account, chat.String(), e.MessageIDs); err != nil {
				slog.Error("whatsapp: storing a read receipt", "chat", chat.String(), "err", err)
			}
		}
	case *events.MarkChatAsRead:
		if account := s.c.Account(); account != "" && e.Action.GetRead() {
			chat := phoneNumber(e.JID.ToNonAD(), types.EmptyJID, s.pnOf)
			if err := s.archive.ReadThrough(ctx, account, chat.String(), e.Timestamp); err != nil {
				slog.Error("whatsapp: storing a chat read elsewhere", "chat", chat.String(), "err", err)
			}
		}
	case *events.PushName:
		if account := s.c.Account(); account != "" && e.NewPushName != "" {
			s.archive.NameChat(ctx, account, e.JID.ToNonAD().String(), e.NewPushName, false)
		}
	}
}

func (s *Service) message(ctx context.Context, e *events.Message) {
	account := s.c.Account()
	if account == "" {
		return
	}
	row, edit, ok := FromMessage(account, e, s.pnOf)
	switch {
	case !ok:
		return
	case edit != nil:
		if err := s.archive.Update(ctx, account, edit.ChatJID, edit.ID, edit.Text); err != nil {
			slog.Error("whatsapp: storing an edit", "chat", edit.ChatJID, "err", err)
		}
		return
	}
	if self, err := types.ParseJID(account); err == nil && !row.FromMe {
		if chat, err := types.ParseJID(row.ChatJID); err == nil {
			row.Unread = private(chat, self)
		}
	}
	if err := s.archive.Save(ctx, row); err != nil {
		slog.Error("whatsapp: storing a message", "chat", row.ChatJID, "id", row.ID, "err", err)
		return
	}
	s.name(ctx, account, row.ChatJID, e)
}

func (s *Service) pnOf(lid types.JID) (types.JID, bool) {
	return s.c.PNForLID(context.Background(), lid)
}

// name gives a chat its name: a group's from WhatsApp, once per run; a contact's from its push name.
func (s *Service) name(ctx context.Context, account, chatJID string, e *events.Message) {
	chat, _ := types.ParseJID(chatJID)
	if chat.Server == types.GroupServer {
		s.mu.Lock()
		done := s.named[chat.String()]
		s.named[chat.String()] = true
		s.mu.Unlock()
		if done {
			return
		}
		name, err := s.c.GroupName(ctx, chat.String())
		if err != nil {
			slog.Warn("whatsapp: group name", "chat", chat.String(), "err", err)
		}
		s.archive.NameChat(ctx, account, chat.String(), name, true)
		return
	}
	if !e.Info.IsFromMe && e.Info.PushName != "" {
		s.archive.NameChat(ctx, account, chat.String(), e.Info.PushName, false)
	}
}

func (s *Service) history(ctx context.Context, h *events.HistorySync) {
	account := s.c.Account()
	if account == "" {
		return
	}
	rows, names := FromHistory(account, h, s.c.ParseWebMessage, s.pnOf)
	for _, r := range rows {
		if err := s.archive.Save(ctx, r); err != nil {
			slog.Error("whatsapp: storing history", "chat", r.ChatJID, "err", err)
		}
	}
	for jid, name := range names {
		parsed, _ := types.ParseJID(jid)
		s.archive.NameChat(ctx, account, jid, name, parsed.Server == types.GroupServer)
	}
}
