package converse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/h2c"
	"concierge/engine/internal/listen"

	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// phonePoll is how often the owner's Contacts phone is read again for the listener.
	phonePoll = 30 * time.Second
	// maxAge is how old a message may be when it arrives: a box that was offline for long must not act
	// on requests it was sent meanwhile.
	maxAge = 10 * time.Minute
)

// WhatsAppChannel sends to the owner's WhatsApp: whatsappd's send, to the owner's Contacts phone read
// fresh for each send, never to a chat anyone else names.
type WhatsAppChannel struct {
	settings Settings
	http     *http.Client
}

func NewWhatsAppChannel(socket string, s Settings) *WhatsAppChannel {
	return &WhatsAppChannel{settings: s, http: &http.Client{Transport: h2c.Transport(socket), Timeout: 30 * time.Second}}
}

var _ Channel = (*WhatsAppChannel)(nil)

var errNoPhone = errors.New("the owner's phone is not set in Contacts")

// partRunes is the longest part one send carries: whatsappd refuses a text over 4096 runes.
const partRunes = 4000

// Send sends the text to the owner's phone, a long one in parts, in order; it fails, sending no more,
// at the first part that fails: the text counts as sent only once every part is.
func (c *WhatsAppChannel) Send(ctx context.Context, text string) error {
	phone, err := c.settings.OwnerPhone(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(phone) == "" {
		return errNoPhone
	}
	for _, part := range sendParts(text) {
		if err := c.send(ctx, phone, part); err != nil {
			return err
		}
	}
	return nil
}

// sendParts cuts text into parts of at most partRunes runes: at the last paragraph break that fits,
// else the last line break, else the last space, else hard at the limit. The break itself is dropped.
func sendParts(text string) []string {
	var parts []string
	for utf8.RuneCountInString(text) > partRunes {
		head := string([]rune(text)[:partRunes])
		cut, skip := len(head), 0
		for _, sep := range []string{"\n\n", "\n", " "} {
			if i := strings.LastIndex(head, sep); i > 0 {
				cut, skip = i, len(sep)
				break
			}
		}
		parts = append(parts, text[:cut])
		text = text[cut+skip:]
	}
	return append(parts, text)
}

func (c *WhatsAppChannel) send(ctx context.Context, phone, text string) error {
	body, _ := json.Marshal(map[string]string{"chat": phone, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://whatsappd/whatsapp/send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusOK {
		return fmt.Errorf("whatsapp send: %s", res.Status)
	}
	return nil
}

// owners is the hub, as the listener uses it.
type owners interface {
	Owner(ctx context.Context, channel, thread, text string, src Source)
	LastHeard(ctx context.Context, channel string) (time.Time, bool, error)
}

// ListenWhatsApp gives the hub the owner's WhatsApp messages until ctx ends: it reads the owner's
// Contacts phone every 30 s and keeps one listener of whatsappd open for that phone's digits,
// re-opening it when the phone changes and closing it with no phone. Each listener resumes after the
// newest message the hub took in, so what came while the engine was down is taken in once (the hub
// takes in a replayed one nothing more), but never from before maxAge ago. A message reaches the hub
// only while the WhatsApp switch is on, and only when fresh. Message text is never logged.
func ListenWhatsApp(ctx context.Context, l listen.Listener, h *Hub, s Settings) {
	listenWhatsApp(ctx, l, h, s, time.Now, phonePoll)
}

// resumeAt is where a listener of the channel replays from: the newest message the hub took in, or
// maxAge ago when that is later (or none was, or it can't be read).
func resumeAt(ctx context.Context, h owners, channel string, now time.Time) time.Time {
	since := now.Add(-maxAge)
	last, ok, err := h.LastHeard(ctx, channel)
	if err != nil {
		slog.Warn("converse: reading where the listener left off", "channel", channel, "err", err)
	}
	if ok && last.After(since) {
		return last
	}
	return since
}

func listenWhatsApp(ctx context.Context, l listen.Listener, h owners, s Settings, now func() time.Time, every time.Duration) {
	var (
		current string // the digits listened for
		stop    context.CancelFunc
		done    chan struct{}
	)
	closeListener := func() {
		if stop != nil {
			stop()
			<-done
			stop, done = nil, nil
		}
	}
	defer closeListener()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		phone, err := s.OwnerPhone(ctx)
		if err != nil {
			slog.Warn("converse: reading the owner's phone", "err", err)
		} else if d := digits(phone); d != current {
			closeListener()
			current = d
			if d != "" {
				lctx, cancel := context.WithCancel(ctx)
				fin := make(chan struct{})
				stop, done = cancel, fin
				since := timestamppb.New(resumeAt(ctx, h, "whatsapp", now()))
				go func() {
					defer close(fin)
					for e := range l.Listen(lctx, &listenv1.ListenRequest{From: d, Since: since}) {
						if m := e.GetMessage(); m != nil {
							deliver(lctx, h, s, now, d, m)
						}
					}
				}()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// maxAhead is how far a message's sent_at may be in the future (clock skew).
const maxAhead = time.Minute

// deliver gives the hub one message of the listener opened for the owner's digits: only the owner's
// own, fresh, with a sent_at, while the listener is still the current one and the switch is on.
func deliver(ctx context.Context, h owners, s Settings, now func() time.Time, owner string, m *listenv1.Message) {
	w := m.GetWhatsapp()
	if w == nil || ctx.Err() != nil {
		return
	}
	if digits(w.GetPhone()) != owner {
		slog.Warn("converse: dropped a WhatsApp message not from the owner's phone")
		return
	}
	if w.GetSentAt() == nil {
		slog.Warn("converse: dropped a WhatsApp message with no send time")
		return
	}
	age := now().Sub(w.GetSentAt().AsTime())
	if age > maxAge || age < -maxAhead {
		slog.Info("converse: dropped a WhatsApp message sent too long ago or in the future")
		return
	}
	// Switched off: dropped. A switch that can't be read is not off: the hub keeps the message and
	// decides once it can read the settings.
	_, on, err := s.Answering(ctx)
	if err != nil {
		slog.Warn("converse: reading the WhatsApp switch; the message is kept", "err", err)
	} else if !on {
		return
	}
	if ctx.Err() != nil {
		return
	}
	h.Owner(ctx, "whatsapp", w.GetChat(), w.GetText(), sourceOf(m))
}

// sourceOf is the WhatsApp message a listen message carries, known by its chat and WhatsApp's own
// id: whatsappd's row id (Message.id) is numbered again after its archive is wiped, so it would take
// a new message for one heard before. One with no WhatsApp id is no source (nothing to know it by).
func sourceOf(m *listenv1.Message) Source {
	w := m.GetWhatsapp()
	if w.GetId() == "" {
		return Source{}
	}
	return Source{Channel: "whatsapp", ID: w.GetChat() + "/" + w.GetId(), StoredAt: m.GetStoredAt().AsTime()}
}

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}
