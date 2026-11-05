package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"

	"concierge/engine/internal/verify"
)

// NewHandler is whatsappd's API; the engine forwards /api/v1/whatsapp/* here with /api/v1 stripped.
func NewHandler(s *Service) http.Handler {
	mux := http.NewServeMux()
	// The setup check: a message to the account's own chat, answered there from the phone.
	verify.Routes(mux, "/whatsapp", verify.Daemon{
		Send: func(ctx context.Context, _, text string) (string, error) {
			self, ok := s.SelfChat()
			if !ok {
				return "", fmt.Errorf("whatsapp: %w", verify.ErrNotReady)
			}
			if err := s.Send(ctx, self, text); errors.Is(err, ErrNotConnected) {
				return "", fmt.Errorf("whatsapp: %w", verify.ErrNotReady)
			} else if err != nil {
				return "", err
			}
			return self, nil
		},
		Replies: func(ctx context.Context, self string, since time.Time) ([]string, error) {
			msgs, err := s.Messages(ctx, self)
			var out []string
			for _, m := range msgs {
				if m.SentAt >= ts(since) {
					out = append(out, m.Text)
				}
			}
			return out, err
		},
		IntN: rand.IntN,
		Now:  time.Now,
	})
	mux.HandleFunc("GET /whatsapp/status", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "status", http.StatusOK, s.Status(), nil)
	})
	mux.HandleFunc("POST /whatsapp/link", func(w http.ResponseWriter, r *http.Request) {
		reply(w, "link", http.StatusAccepted, struct{}{}, s.Link(r.Context()))
	})
	mux.HandleFunc("GET /whatsapp/qr", func(w http.ResponseWriter, _ *http.Request) {
		code, expires, ok := s.QR()
		if !ok {
			reply(w, "qr", 0, nil, errNoQR)
			return
		}
		reply(w, "qr", http.StatusOK, map[string]any{"code": code, "expires_in": int(expires.Round(time.Second) / time.Second)}, nil)
	})
	mux.HandleFunc("POST /whatsapp/unlink", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DeleteChats bool `json:"delete_chats"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "unlink", 0, nil, errBadBody)
			return
		}
		reply(w, "unlink", http.StatusNoContent, nil, s.Unlink(r.Context(), in.DeleteChats))
	})
	mux.HandleFunc("POST /whatsapp/remove", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DeleteData bool `json:"delete_data"`
			Force      bool `json:"force"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "remove", 0, nil, errBadRemove)
			return
		}
		reply(w, "remove", http.StatusNoContent, nil, s.Remove(r.Context(), in.DeleteData, in.Force))
	})
	mux.HandleFunc("POST /whatsapp/send", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Chat string `json:"chat"`
			Text string `json:"text"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "send", 0, nil, errBadSend)
			return
		}
		reply(w, "send", http.StatusNoContent, nil, s.Send(r.Context(), in.Chat, in.Text))
	})
	mux.HandleFunc("GET /whatsapp/chats", func(w http.ResponseWriter, r *http.Request) {
		chats, err := s.Chats(r.Context())
		reply(w, "chats", http.StatusOK, chats, err)
	})
	mux.HandleFunc("GET /whatsapp/messages", func(w http.ResponseWriter, r *http.Request) {
		chat := r.URL.Query().Get("chat")
		if chat == "" {
			reply(w, "messages", 0, nil, errNoChat)
			return
		}
		msgs, err := s.Messages(r.Context(), chat)
		reply(w, "messages", http.StatusOK, msgs, err)
	})
	mux.HandleFunc("GET /whatsapp/unread", func(w http.ResponseWriter, r *http.Request) {
		n, err := s.Unread(r.Context())
		reply(w, "unread", http.StatusOK, map[string]int{"count": n}, err)
	})
	mux.HandleFunc("POST /whatsapp/read", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Chat string `json:"chat"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || in.Chat == "" {
			reply(w, "read", 0, nil, errNoChat)
			return
		}
		reply(w, "read", http.StatusNoContent, nil, s.MarkRead(r.Context(), in.Chat))
	})
	return mux
}

var (
	errNoQR      = errors.New("no QR code: not pairing")
	errBadBody   = errors.New(`body must be {"delete_chats": true|false}`)
	errBadRemove = errors.New(`body must be {"delete_data": true|false, "force": true|false}`)
	errNoChat    = errors.New("chat is required")
)

func reply(w http.ResponseWriter, op string, status int, body any, err error) {
	switch {
	case err == nil:
	case errors.Is(err, ErrState), errors.Is(err, ErrNotConnected):
		status, body = http.StatusConflict, map[string]string{"error": err.Error()}
	case errors.Is(err, ErrLogout):
		slog.Warn("whatsapp: logout failed", "op", op, "err", err)
		// 424, not 503: the engine's proxy answers 503 when this daemon is unreachable.
		status, body = http.StatusFailedDependency, map[string]string{"error": ErrLogout.Error()}
	case errors.Is(err, errNoQR):
		status, body = http.StatusNotFound, map[string]string{"error": err.Error()}
	case errors.Is(err, errBadBody), errors.Is(err, errBadRemove), errors.Is(err, errNoChat), errors.Is(err, errBadSend):
		status, body = http.StatusBadRequest, map[string]string{"error": err.Error()}
	case op == "send":
		// WhatsApp's error can name the chat: its type only.
		slog.Warn("whatsapp: sending", "err_type", fmt.Sprintf("%T", err))
		status, body = http.StatusBadGateway, map[string]string{"error": "message not sent"}
	default:
		slog.Error("whatsapp: request failed", "op", op, "err", err)
		status, body = http.StatusBadGateway, map[string]string{"error": "WhatsApp request failed"}
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
