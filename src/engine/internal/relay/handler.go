package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"concierge/engine/internal/secrets"
)

// maxLabel is a device label's length: X.509's upper bound for a common name.
const maxLabel = 64

// ClaimFinishTimeout bounds a claim's finish, which runs on past its request; relayd's shutdown
// waits longer than this for the socket's requests.
const ClaimFinishTimeout = 30 * time.Second

var errBadBody = errors.New("bad request")

// NewHandler is relayd's API on its socket; the engine forwards /api/v1/relay/* here with /api/v1
// stripped. closeDevice closes a revoked device's open connections (the front's CloseDevice).
// Nothing it logs carries the email, the code, a device's password or its file.
func NewHandler(s *Service, ca *CA, closeDevice func(serial string)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /relay/status", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, "status", http.StatusOK, s.Status(), nil)
	})
	mux.HandleFunc("POST /relay/claim/start", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Email string `json:"email"`
		}
		if !decode(w, r, &in) || len(in.Email) > 254 || !strings.Contains(in.Email, "@") {
			answer(w, "claim start", 0, nil, errBadBody)
			return
		}
		answer(w, "claim start", http.StatusNoContent, nil, s.ClaimStart(r.Context(), in.Email))
	})
	mux.HandleFunc("POST /relay/claim/finish", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Code string `json:"code"`
		}
		if !decode(w, r, &in) || in.Code == "" || len(in.Code) > 64 {
			answer(w, "claim finish", 0, nil, errBadBody)
			return
		}
		// A claim the control plane accepts is the box's whether or not the console still waits for
		// the answer: the finish runs on a context of its own, so the browser going away mid-call
		// can't lose it.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), ClaimFinishTimeout)
		defer cancel()
		answer(w, "claim finish", http.StatusNoContent, nil, s.ClaimFinish(ctx, in.Code))
	})
	mux.HandleFunc("GET /relay/devices", func(w http.ResponseWriter, r *http.Request) {
		ds, err := s.Devices(r.Context())
		if ds == nil {
			ds = []Device{}
		}
		answer(w, "devices", http.StatusOK, ds, err)
	})
	mux.HandleFunc("POST /relay/devices", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Label string `json:"label"`
		}
		label := ""
		if decode(w, r, &in) {
			label = strings.TrimSpace(in.Label)
		}
		if label == "" || utf8.RuneCountInString(label) > maxLabel {
			answer(w, "issue", 0, nil, errBadBody)
			return
		}
		serial, p12, password, expires, err := ca.Issue(label)
		var profile []byte
		if err == nil {
			profile, err = Profile(label, serial, p12)
		}
		if err == nil {
			err = s.AddDevice(r.Context(), serial, label, expires)
		}
		if err != nil {
			answer(w, "issue", 0, nil, err)
			return
		}
		// The password and the key are in this answer alone: nothing on the way may keep it.
		w.Header().Set("Cache-Control", "no-store")
		// The same file twice: as a .p12 (Android, others) and inside an Apple profile (.mobileconfig).
		answer(w, "issue", http.StatusOK, struct {
			Serial       string    `json:"serial"`
			P12          string    `json:"p12"`
			MobileConfig string    `json:"mobileconfig"`
			Password     string    `json:"password"`
			Expires      time.Time `json:"expires"`
		}{serial, base64.StdEncoding.EncodeToString(p12), base64.StdEncoding.EncodeToString(profile), password, expires}, nil)
	})
	mux.HandleFunc("DELETE /relay/devices/{serial}", func(w http.ResponseWriter, r *http.Request) {
		serial := r.PathValue("serial")
		if err := s.RemoveDevice(r.Context(), serial); err != nil {
			answer(w, "revoke", 0, nil, err)
			return
		}
		closeDevice(serial) // after the removal: a connection opened meanwhile is refused anyway
		answer(w, "revoke", http.StatusNoContent, nil, nil)
	})
	return mux
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil
}

func answer(w http.ResponseWriter, op string, status int, body any, err error) {
	switch {
	case err == nil:
	case errors.Is(err, errBadBody), errors.Is(err, ErrWrongCode), errors.Is(err, ErrExpiredCode), errors.Is(err, ErrNoCode):
		status, body = http.StatusBadRequest, map[string]string{"error": err.Error()}
	case errors.Is(err, ErrAlreadyClaimed), errors.Is(err, errNotClaiming), errors.Is(err, errNotClaimed):
		status, body = http.StatusConflict, map[string]string{"error": err.Error()}
	case errors.Is(err, secrets.ErrUnavailable):
		status, body = http.StatusBadGateway, map[string]string{"error": secrets.ErrUnavailable.Error()}
	case errors.Is(err, ErrUnreachable):
		status, body = http.StatusServiceUnavailable, map[string]string{"error": ErrUnreachable.Error()}
	default:
		// The Cloud's, the store's and the CA's errors carry no email, code or secret.
		slog.Error("relay: request failed", "op", op, "err", err)
		status, body = http.StatusBadGateway, map[string]string{"error": "remote access failed"}
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
