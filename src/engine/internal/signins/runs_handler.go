package signins

import (
	"errors"
	"net/http"
	"strconv"
)

// NewRunsHandler is the console's runs API behind the login: every agent run of any kind (errand,
// registration, sign-in check) under /api/v1/runs, POST /api/v1/errands to start an errand, and what
// errands arranged under /api/v1/appointments.
func NewRunsHandler(svc *Service) http.Handler {
	h := &runsHandler{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/errands", h.start)
	mux.HandleFunc("GET /api/v1/runs", h.list)
	mux.HandleFunc("GET /api/v1/runs/{run}", h.one)
	mux.HandleFunc("POST /api/v1/runs/{run}/answer", h.answer)
	mux.HandleFunc("POST /api/v1/runs/{run}/stop", h.stop)
	mux.HandleFunc("GET /api/v1/runs/{run}/log", h.log)
	mux.HandleFunc("POST /api/v1/runs/{run}/appointment", h.keepAppointment)
	mux.HandleFunc("GET /api/v1/appointments", h.appointments)
	mux.HandleFunc("POST /api/v1/appointments/{id}/check", h.checkAppointment)
	mux.HandleFunc("DELETE /api/v1/appointments/{id}", h.forgetAppointment)
	return mux
}

type runsHandler struct{ svc *Service }

func (h *runsHandler) list(w http.ResponseWriter, r *http.Request) {
	views, err := h.svc.AgentRuns(r.Context())
	if err != nil {
		fail(w, "runs", err)
		return
	}
	reply(w, http.StatusOK, views)
}

func (h *runsHandler) start(w http.ResponseWriter, r *http.Request) {
	var body Errand
	if !decode(w, r, &body) {
		return
	}
	id, err := h.svc.StartErrand(r.Context(), body)
	if err != nil {
		runFail(w, "start errand", err)
		return
	}
	reply(w, http.StatusAccepted, map[string]int64{"run": id})
}

func (h *runsHandler) one(w http.ResponseWriter, r *http.Request) {
	run, ok := runID(w, r)
	if !ok {
		return
	}
	d, found, err := h.svc.AgentRun(r.Context(), run)
	if err == nil && !found {
		err = ErrUnknownSite
	}
	if err != nil {
		runFail(w, "run", err)
		return
	}
	reply(w, http.StatusOK, d)
}

func (h *runsHandler) answer(w http.ResponseWriter, r *http.Request) {
	run, ok := runID(w, r)
	if !ok {
		return
	}
	var body struct{ Answer string }
	if !decode(w, r, &body) {
		return
	}
	if err := h.svc.Answer(run, body.Answer); err != nil {
		runFail(w, "answer", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *runsHandler) stop(w http.ResponseWriter, r *http.Request) {
	run, ok := runID(w, r)
	if !ok {
		return
	}
	if err := h.svc.Stop(run); err != nil {
		runFail(w, "stop", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// log is the run's browser-use log, as plain text the owner opens in a tab of its own.
func (h *runsHandler) log(w http.ResponseWriter, r *http.Request) {
	run, ok := runID(w, r)
	if !ok {
		return
	}
	path, found, err := h.svc.AgentRunLog(r.Context(), run)
	if err != nil {
		runFail(w, "run log", err)
		return
	}
	if !found {
		reply(w, http.StatusNotFound, map[string]string{"error": "this run has no log"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, path)
}

// keepAppointment keeps what a done errand arranged, read from its words: 201 with the appointment.
func (h *runsHandler) keepAppointment(w http.ResponseWriter, r *http.Request) {
	run, ok := runID(w, r)
	if !ok {
		return
	}
	a, err := h.svc.KeepAppointment(r.Context(), run)
	if err != nil {
		runFail(w, "keep appointment", err)
		return
	}
	reply(w, http.StatusCreated, a)
}

func (h *runsHandler) appointments(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Appointments(r.Context(), "")
	if err != nil {
		runFail(w, "appointments", err)
		return
	}
	reply(w, http.StatusOK, list)
}

func (h *runsHandler) checkAppointment(w http.ResponseWriter, r *http.Request) {
	id, ok := appointmentID(w, r)
	if !ok {
		return
	}
	run, err := h.svc.CheckAppointment(r.Context(), id)
	if err != nil {
		runFail(w, "check appointment", err)
		return
	}
	reply(w, http.StatusAccepted, map[string]int64{"run": run})
}

func (h *runsHandler) forgetAppointment(w http.ResponseWriter, r *http.Request) {
	id, ok := appointmentID(w, r)
	if !ok {
		return
	}
	if err := h.svc.ForgetAppointment(r.Context(), id); err != nil {
		runFail(w, "forget appointment", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func appointmentID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		reply(w, http.StatusNotFound, map[string]string{"error": ErrNoAppointment.Error()})
		return 0, false
	}
	return id, true
}

func runID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	run, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
	if err != nil {
		reply(w, http.StatusNotFound, map[string]string{"error": "no such run"})
		return 0, false
	}
	return run, true
}

// runFail is fail with the run's own refusal: a bad link, action or answer is the owner's to fix.
func runFail(w http.ResponseWriter, op string, err error) {
	if errors.Is(err, ErrBadErrand) || errors.Is(err, ErrBadAnswer) {
		reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, ErrUnknownSite) {
		reply(w, http.StatusNotFound, map[string]string{"error": "no such run"})
		return
	}
	if errors.Is(err, ErrNotKeepable) || errors.Is(err, ErrAppointmentKept) {
		reply(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, ErrNothingArranged) {
		reply(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, ErrNoAppointment) {
		reply(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	fail(w, op, err)
}
