package telegram

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

// NewHandler is telegramd's API; the engine forwards /api/v1/telegram/* here with /api/v1 stripped.
func NewHandler(s *Service) http.Handler {
	mux := http.NewServeMux()
	// The setup check: a message to Saved Messages, answered there from the phone.
	verify.Routes(mux, "/telegram", verify.Daemon{
		Send: func(ctx context.Context, _, text string) (string, error) {
			self, ok := s.SelfChat()
			if !ok {
				return "", fmt.Errorf("telegram: %w", verify.ErrNotReady)
			}
			if err := s.Send(ctx, "self", text); errors.Is(err, ErrNotConnected) {
				return "", fmt.Errorf("telegram: %w", verify.ErrNotReady)
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
	mux.HandleFunc("GET /telegram/status", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "status", http.StatusOK, s.Status(), nil)
	})
	mux.HandleFunc("POST /telegram/credentials", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			APIID   int    `json:"api_id"`
			APIHash string `json:"api_hash"`
		}
		if !decode(w, r, &in) || in.APIID <= 0 || in.APIHash == "" {
			reply(w, "credentials", 0, nil, errBadBody)
			return
		}
		reply(w, "credentials", http.StatusNoContent, nil, s.SetCredentials(r.Context(), Creds{ID: in.APIID, Hash: in.APIHash}))
	})
	mux.HandleFunc("POST /telegram/link", func(w http.ResponseWriter, r *http.Request) {
		reply(w, "link", http.StatusAccepted, struct{}{}, s.Link(r.Context()))
	})
	mux.HandleFunc("GET /telegram/qr", func(w http.ResponseWriter, _ *http.Request) {
		url, expires, ok := s.QR()
		if !ok {
			reply(w, "qr", 0, nil, errNoQR)
			return
		}
		reply(w, "qr", http.StatusOK, map[string]any{"url": url, "expires_in": int(expires.Round(time.Second) / time.Second)}, nil)
	})
	mux.HandleFunc("POST /telegram/password", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Password string `json:"password"`
		}
		if !decode(w, r, &in) {
			reply(w, "password", 0, nil, errBadBody)
			return
		}
		reply(w, "password", http.StatusNoContent, nil, s.Password(r.Context(), in.Password))
	})
	mux.HandleFunc("POST /telegram/settings", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ArchiveChannels bool `json:"archive_channels"`
		}
		if !decode(w, r, &in) {
			reply(w, "settings", 0, nil, errBadBody)
			return
		}
		reply(w, "settings", http.StatusNoContent, nil, s.SetArchiveChannels(r.Context(), in.ArchiveChannels))
	})
	mux.HandleFunc("POST /telegram/unlink", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DeleteChats bool `json:"delete_chats"`
		}
		if !decode(w, r, &in) {
			reply(w, "unlink", 0, nil, errBadBody)
			return
		}
		reply(w, "unlink", http.StatusNoContent, nil, s.Unlink(r.Context(), in.DeleteChats))
	})
	mux.HandleFunc("POST /telegram/remove", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DeleteData bool `json:"delete_data"`
			Force      bool `json:"force"`
		}
		if !decode(w, r, &in) {
			reply(w, "remove", 0, nil, errBadBody)
			return
		}
		reply(w, "remove", http.StatusNoContent, nil, s.Remove(r.Context(), in.DeleteData, in.Force))
	})
	mux.HandleFunc("GET /telegram/chats", func(w http.ResponseWriter, r *http.Request) {
		chats, err := s.Chats(r.Context())
		reply(w, "chats", http.StatusOK, chats, err)
	})
	mux.HandleFunc("POST /telegram/send", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Peer string `json:"peer"`
			Text string `json:"text"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "send", 0, nil, errBadSend)
			return
		}
		reply(w, "send", http.StatusNoContent, nil, s.Send(r.Context(), in.Peer, in.Text))
	})
	mux.HandleFunc("GET /telegram/messages", func(w http.ResponseWriter, r *http.Request) {
		chat := r.URL.Query().Get("chat")
		if chat == "" {
			reply(w, "messages", 0, nil, errNoChat)
			return
		}
		msgs, err := s.Messages(r.Context(), chat)
		reply(w, "messages", http.StatusOK, msgs, err)
	})
	mux.HandleFunc("GET /telegram/unread", func(w http.ResponseWriter, r *http.Request) {
		n, err := s.Unread(r.Context())
		reply(w, "unread", http.StatusOK, map[string]int{"count": n}, err)
	})
	mux.HandleFunc("POST /telegram/read", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Peer string `json:"peer"`
		}
		if !decode(w, r, &in) || in.Peer == "" {
			reply(w, "read", 0, nil, errBadBody)
			return
		}
		reply(w, "read", http.StatusNoContent, nil, s.MarkRead(r.Context(), in.Peer))
	})
	return mux
}

var (
	errNoQR    = errors.New("no QR code: not pairing")
	errBadBody = errors.New("malformed request body")
	errNoChat  = errors.New("chat is required")
)

// decode reads a small JSON body with no unknown fields.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil
}

func reply(w http.ResponseWriter, op string, status int, body any, err error) {
	switch {
	case err == nil:
	case errors.Is(err, ErrState), errors.Is(err, ErrNotConnected):
		status, body = http.StatusConflict, map[string]string{"error": err.Error()}
	case errors.Is(err, ErrCredentials), errors.Is(err, ErrPassword):
		status, body = http.StatusUnprocessableEntity, map[string]string{"error": err.Error()}
	case errors.Is(err, ErrLogout):
		slog.Warn("telegram: logout failed", "op", op, "err", err)
		// 424, not 503: the engine's proxy answers 503 when this daemon is unreachable.
		status, body = http.StatusFailedDependency, map[string]string{"error": ErrLogout.Error()}
	case errors.Is(err, errNoQR), errors.Is(err, errUnknownChat):
		status, body = http.StatusNotFound, map[string]string{"error": err.Error()}
	case errors.Is(err, errBadBody), errors.Is(err, errNoChat), errors.Is(err, errBadSend):
		status, body = http.StatusBadRequest, map[string]string{"error": err.Error()}
	case op == "send":
		// Telegram's error can name the peer: its type only.
		slog.Warn("telegram: sending", "err_type", fmt.Sprintf("%T", err))
		status, body = http.StatusBadGateway, map[string]string{"error": "message not sent"}
	default:
		slog.Error("telegram: request failed", "op", op, "err", err)
		status, body = http.StatusBadGateway, map[string]string{"error": "Telegram request failed"}
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
