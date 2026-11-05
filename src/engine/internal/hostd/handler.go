package hostd

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"

	"concierge/engine/internal/secrets"
)

const maxBody = 4096

var (
	uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hexRe  = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	apnRe  = regexp.MustCompile(`^[A-Za-z0-9.-]{1,62}$`)
)

// errBadRequest carries a validation message. Messages name the field, never its value.
type errBadRequest string

func (e errBadRequest) Error() string { return string(e) }

// NewHandler is hostd's API: /network/* changes the box's network
// (docs/superpowers/specs/2026-10-27-settings-network-design.md) and /settings/* holds the box's
// settings. The engine forwards /api/v1/network/* and /api/v1/settings/* here, stripping /api/v1;
// its sign-ins service reads GET /uplink directly (which link is up, so runs stay off mobile data).
func NewHandler(s *Service, st *Settings) http.Handler {
	mux := http.NewServeMux()
	if st != nil {
		settingsRoutes(mux, st)
		callsKeepRoutes(mux, st)
		voiceRoutes(mux, st)
		accountRoutes(mux, st)
		connectorRoutes(mux, s)
	}
	mux.HandleFunc("GET /uplink", func(w http.ResponseWriter, r *http.Request) {
		up, err := s.Uplink(r.Context())
		reply(w, "uplink", http.StatusOK, map[string]string{"uplink": up}, err)
	})
	mux.HandleFunc("GET /network/wifi", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.Wifi(r.Context())
		reply(w, "wifi", http.StatusOK, st, err)
	})
	mux.HandleFunc("POST /network/wifi/scan", func(w http.ResponseWriter, r *http.Request) {
		nets, err := s.Scan(r.Context())
		reply(w, "scan", http.StatusOK, nets, err)
	})
	mux.HandleFunc("POST /network/wifi/test", func(w http.ResponseWriter, r *http.Request) {
		var in network
		if err := decode(w, r, &in); err != nil {
			reply(w, "test", 0, nil, err)
			return
		}
		id, err := s.StartTest(r.Context(), in.SSID, in.Password)
		reply(w, "test", http.StatusAccepted, map[string]string{"test": id}, err)
	})
	mux.HandleFunc("GET /network/wifi/test/{id}", func(w http.ResponseWriter, r *http.Request) {
		res, ok := s.TestResult(r.PathValue("id"))
		if !ok {
			reply(w, "test-result", 0, nil, ErrNotFound)
			return
		}
		reply(w, "test-result", http.StatusOK, res, nil)
	})
	mux.HandleFunc("POST /network/wifi/saved", func(w http.ResponseWriter, r *http.Request) {
		var in network
		if err := decode(w, r, &in); err != nil {
			reply(w, "save", 0, nil, err)
			return
		}
		saved, err := s.Save(r.Context(), in.SSID, in.Password)
		reply(w, "save", http.StatusCreated, saved, err)
	})
	mux.HandleFunc("DELETE /network/wifi/saved/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !uuidRe.MatchString(id) {
			reply(w, "forget", 0, nil, errBadRequest("id must be a connection UUID"))
			return
		}
		cp, err := s.Forget(r.Context(), id)
		reply(w, "forget", http.StatusAccepted, cp, err)
	})
	mux.HandleFunc("POST /network/wifi/connect", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
		}
		err := decode(w, r, &in)
		if err == nil && !uuidRe.MatchString(in.ID) {
			err = errBadRequest("id must be a connection UUID")
		}
		if err != nil {
			reply(w, "connect", 0, nil, err)
			return
		}
		cp, err := s.Connect(r.Context(), in.ID)
		reply(w, "connect", http.StatusAccepted, cp, err)
	})
	mux.HandleFunc("GET /network/cellular", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.Cellular(r.Context())
		reply(w, "cellular", http.StatusOK, st, err)
	})
	mux.HandleFunc("PUT /network/cellular", func(w http.ResponseWriter, r *http.Request) {
		var in CellularUpdate
		err := decode(w, r, &in)
		switch {
		case err != nil:
		case in.APN == nil && in.DataEnabled == nil:
			err = errBadRequest("nothing to change: give apn and/or data_enabled")
		case in.APN != nil && !apnRe.MatchString(*in.APN):
			err = errBadRequest("apn must be 1-62 letters, digits, dots or dashes")
		}
		if err != nil {
			reply(w, "cellular-update", 0, nil, err)
			return
		}
		cp, err := s.UpdateCellular(r.Context(), in)
		reply(w, "cellular-update", http.StatusAccepted, cp, err)
	})
	mux.HandleFunc("GET /network/checkpoints", func(w http.ResponseWriter, r *http.Request) {
		reply(w, "checkpoints", http.StatusOK, map[string]*Checkpoint{"pending": s.Pending()}, nil)
	})
	mux.HandleFunc("POST /network/checkpoints/{id}/confirm", func(w http.ResponseWriter, r *http.Request) {
		reply(w, "confirm", http.StatusNoContent, nil, s.Confirm(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("POST /network/checkpoints/{id}/rollback", func(w http.ResponseWriter, r *http.Request) {
		reply(w, "rollback", http.StatusNoContent, nil, s.Rollback(r.Context(), r.PathValue("id")))
	})
	return mux
}

// network is a Wi-Fi network as the owner types it in.
type network struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

func (n network) validate() error {
	if len(n.SSID) < 1 || len(n.SSID) > 32 {
		return errBadRequest("ssid must be 1-32 bytes")
	}
	if n.Password == "" || hexRe.MatchString(n.Password) {
		return nil
	}
	if len(n.Password) < 8 || len(n.Password) > 63 {
		return errBadRequest("password must be empty (open network), 8-63 characters, or 64 hex digits")
	}
	for _, c := range n.Password {
		if c < 0x20 || c > 0x7e {
			return errBadRequest("password must be printable ASCII")
		}
	}
	return nil
}

func decode(w http.ResponseWriter, r *http.Request, into any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return errBadRequest("body must be a JSON object of the expected fields, at most 4 KiB")
	}
	if n, ok := into.(*network); ok {
		return n.validate()
	}
	return nil
}

// reply writes the result, or maps the error. An error from NetworkManager itself is logged and
// answered with a fixed message: its text is not ours and could carry what the request carried.
func reply(w http.ResponseWriter, op string, status int, body any, err error) {
	var bad errBadRequest
	var unit errUnit
	switch {
	case err == nil:
	case errors.As(err, &unit):
		slog.Error("hostd: systemd request failed", "op", op, "err", err)
		status, body = http.StatusBadGateway, map[string]string{"error": "systemd request failed"}
	case errors.As(err, &bad):
		status, body = http.StatusBadRequest, map[string]string{"error": bad.Error()}
	case errors.Is(err, ErrBusy), errors.Is(err, ErrNotTested), errors.Is(err, ErrActive), errors.Is(err, ErrNoSIM),
		errors.Is(err, ErrNotSettingUp):
		status, body = http.StatusConflict, map[string]string{"error": err.Error()}
	case errors.Is(err, secrets.ErrUnavailable):
		status, body = http.StatusBadGateway, map[string]string{"error": secrets.ErrUnavailable.Error()}
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNoCard):
		status, body = http.StatusNotFound, map[string]string{"error": err.Error()}
	default:
		slog.Error("hostd: network manager request failed", "op", op, "err", err)
		status, body = http.StatusBadGateway, map[string]string{"error": "network manager request failed"}
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
