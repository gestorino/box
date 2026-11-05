package aikey

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"concierge/engine/internal/keyfile"
	"concierge/engine/internal/secrets"
)

// noKey answers a check of no stored key; the sign-ins gate refuses a run in the same words.
const noKey = "no OpenRouter key"

// secretName is the key's name in the engine's namespace in secretd.
const secretName = "openrouter"

// NewHandler is the console's AI page: /api/v1/ai behind the login. GET says whether a key is
// stored and its masked form; PUT checks a key with OpenRouter and keeps it in secretd only when
// OpenRouter accepts it; DELETE removes it; POST check asks OpenRouter about the stored key.
func NewHandler(s *secrets.Client, c *Checker) http.Handler {
	h := &handler{secrets: s, checker: c}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/ai", h.get)
	mux.HandleFunc("PUT /api/v1/ai/key", h.put)
	mux.HandleFunc("DELETE /api/v1/ai/key", h.remove)
	mux.HandleFunc("POST /api/v1/ai/check", h.check)
	return mux
}

// Key reads the stored key from secretd: "" when there is none.
func Key(ctx context.Context, s *secrets.Client) (string, error) {
	key, _, err := s.Get(ctx, secretName)
	return key, err
}

type handler struct {
	secrets *secrets.Client
	checker *Checker
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	key, ok, err := h.secrets.Get(r.Context(), secretName)
	if err != nil {
		failed(w, "reading the key", err)
		return
	}
	view := struct {
		Set    bool   `json:"set"`
		Masked string `json:"masked"`
	}{Set: ok}
	if ok {
		view.Masked = keyfile.Masked(key, "sk-or-")
	}
	reply(w, http.StatusOK, view)
}

func (h *handler) put(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key *string `json:"key"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || in.Key == nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": `body must be {"key": "..."}`})
		return
	}
	key := strings.TrimSpace(*in.Key)
	if !keyfile.WellFormed(key) {
		reply(w, http.StatusBadRequest, map[string]string{"error": "the key must be one line of letters, digits and symbols"})
		return
	}
	if _, err := h.checker.Check(r.Context(), key); err != nil {
		refusal(w, err)
		return
	}
	if err := h.secrets.Put(r.Context(), secretName, key); err != nil {
		failed(w, "storing the key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.secrets.Delete(r.Context(), secretName); err != nil {
		failed(w, "removing the key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) check(w http.ResponseWriter, r *http.Request) {
	key, ok, err := h.secrets.Get(r.Context(), secretName)
	if err != nil {
		failed(w, "reading the key", err)
		return
	}
	if !ok {
		reply(w, http.StatusConflict, map[string]string{"error": noKey})
		return
	}
	credit, err := h.checker.Check(r.Context(), key)
	if errors.Is(err, ErrRefused) {
		reply(w, http.StatusOK, struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}{false, err.Error()})
		return
	}
	if err != nil {
		refusal(w, err)
		return
	}
	reply(w, http.StatusOK, struct {
		OK        bool     `json:"ok"`
		CreditUSD *float64 `json:"credit_usd,omitempty"`
	}{true, credit})
}

// refusal answers a failed check of a key being saved: 422 refused, 502 unreachable.
func refusal(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, ErrRefused) {
		status = http.StatusUnprocessableEntity
	}
	reply(w, status, map[string]string{"error": err.Error()})
}

// failed is secretd not doing what was asked: logged (its errors name the secret, never the key)
// and answered 502 "secrets unavailable".
func failed(w http.ResponseWriter, what string, err error) {
	slog.Error("aikey: "+what, "err", err)
	reply(w, http.StatusBadGateway, map[string]string{"error": secrets.ErrUnavailable.Error()})
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Warn("aikey: response not written", "err", err)
	}
}
