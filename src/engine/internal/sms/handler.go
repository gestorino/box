package sms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"concierge/engine/internal/gen/listen/v1/listenv1connect"
	"concierge/engine/internal/verify"
)

var phoneNumber = regexp.MustCompile(`^\+?\d{3,20}$`)

const maxSMSText = 1600

// NewHandler is smsd's API: reads, the owner's send, and the remove that keeps or wipes the archive
// when the owner removes SMS. The engine forwards /api/v1/sms/* here with /api/v1 stripped. archive
// is the archiver's Tick: a send answers once the sent message is stored, so its thread shows it.
// ls serves the engine's listeners.
func NewHandler(a *Archive, m Modem, archive func(context.Context) error, ls listenv1connect.ListenServiceHandler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(listenv1connect.NewListenServiceHandler(ls))
	mux.HandleFunc("POST /sms/send", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Number string `json:"number"`
			Text   string `json:"text"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields()
		err := dec.Decode(&in)
		number := strings.Join(strings.Fields(in.Number), "")
		if n := utf8.RuneCountInString(in.Text); err != nil || !phoneNumber.MatchString(number) || n < 1 || n > maxSMSText {
			reply(w, "send", nil, errBad(fmt.Sprintf("number must be + and 3–20 digits; text 1–%d characters", maxSMSText)))
			return
		}
		err = m.Send(r.Context(), number, in.Text)
		if err == nil {
			if err := archive(r.Context()); err != nil { // sent all the same: it is stored on a later tick
				slog.Warn("sms: storing the sent message, will retry", "err", fmt.Sprintf("%T", err))
			}
		}
		if err != nil {
			// ModemManager's error names no number nor text, but only its type is logged all the same.
			slog.Error("sms: send failed", "err", fmt.Sprintf("%T", err))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": "SMS not sent"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// The setup check: an SMS to the number the owner typed, answered from that number.
	verify.Routes(mux, "/sms", verify.Daemon{
		To: verify.Phone,
		Send: func(ctx context.Context, number, text string) (string, error) {
			if err := m.Send(ctx, number, text); err != nil {
				return "", err
			}
			if err := archive(ctx); err != nil { // sent all the same: it is stored on a later tick
				slog.Warn("sms: storing the sent check, will retry", "err", fmt.Sprintf("%T", err))
			}
			return number, nil
		},
		Replies: func(ctx context.Context, number string, since time.Time) ([]string, error) {
			thread, err := a.Thread(ctx, number)
			var out []string
			for _, msg := range thread {
				if msg.Direction == "in" && msg.SentAt >= ts(since) {
					out = append(out, msg.Text)
				}
			}
			return out, err
		},
		IntN: rand.IntN,
		Now:  time.Now,
	})
	mux.HandleFunc("GET /sms/conversations", func(w http.ResponseWriter, r *http.Request) {
		c, err := a.Conversations(r.Context())
		reply(w, "conversations", c, err)
	})
	mux.HandleFunc("GET /sms/unread", func(w http.ResponseWriter, r *http.Request) {
		n, err := a.Unread(r.Context())
		reply(w, "unread", map[string]int{"count": n}, err)
	})
	mux.HandleFunc("POST /sms/read", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Number *string `json:"number"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || in.Number == nil {
			reply(w, "read", nil, errBad("body must be {number} (empty for an unknown sender)"))
			return
		}
		if err := a.MarkRead(r.Context(), *in.Number); err != nil {
			reply(w, "read", nil, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /sms/thread", func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.Query().Has("number") {
			reply(w, "thread", nil, errBad("number is required (empty for an unknown sender)"))
			return
		}
		m, err := a.Thread(r.Context(), r.URL.Query().Get("number"))
		reply(w, "thread", m, err)
	})
	mux.HandleFunc("GET /sms/recent", func(w http.ResponseWriter, r *http.Request) {
		minutes, err := strconv.Atoi(r.URL.Query().Get("minutes"))
		if err != nil || minutes < 1 || minutes > 24*60 {
			reply(w, "recent", nil, errBad("minutes must be 1–1440"))
			return
		}
		m, err := a.Recent(r.Context(), time.Duration(minutes)*time.Minute)
		reply(w, "recent", m, err)
	})
	mux.HandleFunc("POST /sms/remove", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DeleteData bool `json:"delete_data"`
			Force      bool `json:"force"` // nothing to log out of here
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "remove", nil, errBad("body must be {delete_data, force}"))
			return
		}
		if in.DeleteData {
			if err := a.Wipe(r.Context()); err != nil {
				reply(w, "remove", nil, err)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

type errBad string

func (e errBad) Error() string { return string(e) }

// reply writes JSON; a store error is logged without any message content and answered with a fixed
// message.
func reply(w http.ResponseWriter, op string, body any, err error) {
	status := http.StatusOK
	var bad errBad
	switch {
	case err == nil:
	case errors.As(err, &bad):
		status, body = http.StatusBadRequest, map[string]string{"error": bad.Error()}
	default:
		slog.Error("sms: request failed", "op", op, "err", err)
		status, body = http.StatusBadGateway, map[string]string{"error": "SMS archive unavailable"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
