package signins

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"concierge/engine/internal/browser"
	"concierge/engine/internal/secrets"
)

// maxBody bounds a request body: a link, or nothing.
const maxBody = 256 << 10

// NewHandler is the console's Accounts API, /api/v1/accounts, and the run machinery's own routes
// under /api/v1/signins/sites/{site} (the live screen, Done and Give up, a run's recording), all
// behind the login. No response carries a password: SiteView has no field for one.
func NewHandler(svc *Service) http.Handler {
	h := &handler{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/accounts", h.list)
	mux.HandleFunc("POST /api/v1/accounts/suggest", h.suggest)
	mux.HandleFunc("POST /api/v1/accounts", h.create)
	mux.HandleFunc("PUT /api/v1/accounts/{key}", h.update)
	mux.HandleFunc("POST /api/v1/accounts/{key}/register", h.register)
	mux.HandleFunc("POST /api/v1/accounts/{key}/check", h.check)
	mux.HandleFunc("GET /api/v1/accounts/{key}", h.site)
	mux.HandleFunc("GET /api/v1/accounts/{key}/icon", h.icon)
	mux.HandleFunc("POST /api/v1/accounts/{key}/remove", h.removeAccount)
	mux.HandleFunc("DELETE /api/v1/accounts/{key}", h.removeCard)
	mux.HandleFunc("GET /api/v1/signins/sites/{site}/screen/{path...}", h.screen)
	mux.HandleFunc("POST /api/v1/signins/sites/{site}/live/done", h.answer(h.svc.Done))
	mux.HandleFunc("POST /api/v1/signins/sites/{site}/live/give-up", h.answer(h.svc.GiveUp))
	mux.HandleFunc("GET /api/v1/signins/sites/{site}/runs/{id}/recording/{n}", h.recording)
	return mux
}

type handler struct{ svc *Service }

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	sites, err := h.svc.Sites(r.Context())
	if err != nil {
		fail(w, "list", err)
		return
	}
	reply(w, http.StatusOK, map[string]any{"sites": sites, "settings": h.svc.Settings(r.Context())})
}

// site is one card's page: the card, the box's account on it, its runs, and the card as its edit
// page fills it (`card`, its icon included).
func (h *handler) site(w http.ResponseWriter, r *http.Request) {
	v, runs, found, err := h.svc.Site(r.Context(), r.PathValue("key"))
	var card CardInput
	var appointments []Appointment
	if err == nil && found {
		card, found, err = h.svc.EditableCard(r.Context(), r.PathValue("key"))
	}
	if err == nil && found {
		appointments, err = h.svc.Appointments(r.Context(), v.Key)
	}
	if err == nil && !found {
		err = ErrUnknownSite
	}
	if err != nil {
		fail(w, "site", err)
		return
	}
	reply(w, http.StatusOK, map[string]any{"site": v, "runs": runs, "card": card, "appointments": appointments})
}

// suggest is Add's first step: {"link"} → the card suggested for it, not stored (or the site's own,
// `exists`); with "again", the edit page's fresh read of a carded site.
func (h *handler) suggest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Link  string `json:"link"`
		Again bool   `json:"again"`
	}
	if !decode(w, r, &in) {
		return
	}
	suggest := h.svc.Suggest
	if in.Again {
		suggest = h.svc.SuggestAgain
	}
	sg, err := suggest(r.Context(), in.Link)
	if err != nil {
		fail(w, "suggest", err)
		return
	}
	reply(w, http.StatusOK, sg)
}

// create keeps the owner's new card: 201 with the site.
func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in CardInput
	if !decode(w, r, &in) {
		return
	}
	v, err := h.svc.CreateCard(r.Context(), in)
	if err != nil {
		fail(w, "create card", err)
		return
	}
	reply(w, http.StatusCreated, map[string]SiteView{"site": v})
}

// update writes the owner's edit of a card.
func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var in CardInput
	if !decode(w, r, &in) {
		return
	}
	v, err := h.svc.UpdateCard(r.Context(), r.PathValue("key"), in)
	if err != nil {
		fail(w, "update card", err)
		return
	}
	reply(w, http.StatusOK, map[string]SiteView{"site": v})
}

// register is the owner's Register: 202 with the registration's run once it has started.
func (h *handler) register(w http.ResponseWriter, r *http.Request) {
	id, err := h.svc.Register(r.Context(), r.PathValue("key"))
	if err != nil {
		fail(w, "register", err)
		return
	}
	reply(w, http.StatusAccepted, map[string]int64{"run": id})
}

// check is the owner's Check sign-in: 202 with the check's run once it has started.
func (h *handler) check(w http.ResponseWriter, r *http.Request) {
	id, err := h.svc.CheckSignIn(r.Context(), r.PathValue("key"))
	if err != nil {
		fail(w, "check", err)
		return
	}
	reply(w, http.StatusAccepted, map[string]int64{"run": id})
}

// icon serves a card's icon. It is a PNG the card was checked to carry, so it is served as one and
// never sniffed as anything else.
func (h *handler) icon(w http.ResponseWriter, r *http.Request) {
	png, err := h.svc.Icon(r.Context(), r.PathValue("key"))
	if err != nil {
		fail(w, "icon", err)
		return
	}
	if png == nil {
		reply(w, http.StatusNotFound, map[string]string{"error": "no icon"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(png)
}

func (h *handler) removeAccount(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RemoveAccount(r.Context(), r.PathValue("key")); err != nil {
		fail(w, "remove", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) removeCard(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RemoveCard(r.Context(), r.PathValue("key")); err != nil {
		fail(w, "remove card", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// answer is Done or Give up: 204, or 409 when no run on the site is waiting for the owner.
func (h *handler) answer(do func(string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := do(r.PathValue("site")); err != nil {
			fail(w, "live answer", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "body must be a JSON object of the expected fields, at most 256 KiB"})
		return false
	}
	return true
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Warn("signins: response not written", "err", err)
	}
}

// fail maps a service error to its status. What the service did not name is logged with the route
// and answered with a fixed message: its text is not ours to echo.
func fail(w http.ResponseWriter, op string, err error) {
	var notReady *NotReadyError
	msg := func(status int, m string) { reply(w, status, map[string]string{"error": m}) }
	switch {
	case errors.Is(err, ErrBadCard):
		msg(http.StatusBadRequest, strings.TrimPrefix(err.Error(), ErrBadCard.Error()+": "))
	case errors.Is(err, ErrCardExists):
		msg(http.StatusConflict, err.Error())
	case errors.Is(err, ErrBadErrand):
		msg(http.StatusBadRequest, "a link must be an http(s) URL of a site")
	case errors.As(err, &notReady):
		reply(w, http.StatusConflict, map[string]any{"error": err.Error(), "missing": notReady.Missing})
	case errors.Is(err, ErrBusy), errors.Is(err, ErrWaitingForWiFi), errors.Is(err, ErrNoAccount), errors.Is(err, ErrShuttingDown),
		errors.Is(err, ErrNoKey), errors.Is(err, ErrCardInUse), errors.Is(err, ErrAccountExists), errors.Is(err, ErrNoPassword):
		msg(http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotWaiting):
		msg(http.StatusConflict, "not waiting for you")
	case errors.Is(err, ErrUnknownSite):
		msg(http.StatusNotFound, err.Error())
	case errors.Is(err, browser.ErrUnavailable):
		msg(http.StatusServiceUnavailable, "the browser worker is not available")
	case errors.Is(err, secrets.ErrUnavailable):
		slog.Error("signins: request failed", "op", op, "err", err)
		msg(http.StatusBadGateway, secrets.ErrUnavailable.Error())
	default:
		slog.Error("signins: request failed", "op", op, "err", err)
		msg(http.StatusBadGateway, "accounts request failed")
	}
}
