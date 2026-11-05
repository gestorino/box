package hostd

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
)

// The box's channels, each a daemon behind a socket-activated systemd unit pair. Only added ones
// run: hostd starts and stops them (a polkit rule grants exactly these units' start and stop) and
// reconciles them against the list on every start. Design:
// docs/superpowers/specs/2026-10-28-connectors-design.md.

var (
	ErrNoSIM        = errors.New("needs a SIM — none in the box")
	ErrNotSettingUp = errors.New("not being set up")
)

// Units starts and stops a systemd unit by its full name (concierge-sms.socket).
type Units interface {
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
}

// Connector is one channel and its state: not_added, setting_up (its daemon runs; the owner hasn't
// answered its check yet) or added (verified: it is in the sidebar).
type Connector struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	NeedsSIM bool   `json:"needs_sim"`
}

type Connectors struct {
	SIM        bool        `json:"sim"`
	Connectors []Connector `json:"connectors"`
}

// connectors in the sidebar's order, with the unit each runs as.
type connectorUnit struct {
	name, unit string
	needsSIM   bool
}

var connectors = []connectorUnit{
	{"email", "concierge-email", false},
	{"voice", "concierge-calls", true},
	{"sms", "concierge-sms", true},
	{"whatsapp", "concierge-whatsapp", false},
	{"telegram", "concierge-telegram", false},
}

func unitOf(name string) (unit string, needsSIM bool, err error) {
	i := slices.IndexFunc(connectors, func(c connectorUnit) bool { return c.name == name })
	if i < 0 {
		return "", false, ErrNotFound
	}
	return connectors[i].unit, connectors[i].needsSIM, nil
}

// sim reports a SIM in the box; no modem counts as none.
func (s *Service) sim(ctx context.Context) bool {
	_, sim, err := s.mm.Modem(ctx)
	return err == nil && sim.Present
}

func (s *Service) Connectors(ctx context.Context) (Connectors, error) {
	states, err := s.st.States(ctx)
	if err != nil {
		return Connectors{}, err
	}
	out := Connectors{SIM: s.sim(ctx)}
	for _, c := range connectors {
		state := states[c.name]
		if state == "" {
			state = "not_added"
		}
		out.Connectors = append(out.Connectors, Connector{Name: c.name, State: state, NeedsSIM: c.needsSIM})
	}
	return out, nil
}

// AddConnector starts the socket, then the service, and only then lists it as setting up (one
// already added stays added): an add that cannot start leaves nothing behind.
func (s *Service) AddConnector(ctx context.Context, name string) error {
	unit, needsSIM, err := unitOf(name)
	if err != nil {
		return err
	}
	if needsSIM && !s.sim(ctx) {
		return ErrNoSIM
	}
	if err := s.start(ctx, unit); err != nil {
		return err
	}
	return s.st.Begin(ctx, name)
}

// VerifyConnector marks a connector being set up as added. Only the engine calls it, once the
// connector's daemon reports its check answered; a connector removed meanwhile stays removed.
func (s *Service) VerifyConnector(ctx context.Context, name string) error {
	if _, _, err := unitOf(name); err != nil {
		return err
	}
	ok, err := s.st.Verify(ctx, name)
	if err == nil && !ok {
		err = ErrNotSettingUp
	}
	return err
}

// RemoveConnector stops the service, then the socket (a live socket would start it again), and
// forgets it. The daemon's own data was kept or wiped before, by its remove.
func (s *Service) RemoveConnector(ctx context.Context, name string) error {
	unit, _, err := unitOf(name)
	if err != nil {
		return err
	}
	if err := s.stop(ctx, unit); err != nil {
		return err
	}
	return s.st.Forget(ctx, name)
}

// Reconcile makes the units match the list: listed ones (setting up or added) running, the rest
// stopped. A unit that
// fails is logged and the rest still reconciled — hostd keeps serving the network regardless.
func (s *Service) Reconcile(ctx context.Context) {
	states, err := s.st.States(ctx)
	if err != nil {
		slog.Error("hostd: reading the connectors", "err", err)
		return
	}
	for _, c := range connectors {
		op, verb := s.stop, "stop"
		if states[c.name] != "" {
			op, verb = s.start, "start"
		}
		if err := op(ctx, c.unit); err != nil {
			slog.Error("hostd: reconciling a connector", "connector", c.name, "op", verb, "err", err)
		}
	}
}

// errUnit is systemd failing a start or stop; the handler answers it as such, not as NetworkManager.
type errUnit struct{ err error }

func (e errUnit) Error() string { return e.err.Error() }
func (e errUnit) Unwrap() error { return e.err }

func (s *Service) start(ctx context.Context, unit string) error {
	if err := s.units.Start(ctx, unit+".socket"); err != nil {
		return errUnit{err}
	}
	if err := s.units.Start(ctx, unit+".service"); err != nil {
		return errUnit{err}
	}
	return nil
}

func (s *Service) stop(ctx context.Context, unit string) error {
	if err := s.units.Stop(ctx, unit+".service"); err != nil {
		return errUnit{err}
	}
	if err := s.units.Stop(ctx, unit+".socket"); err != nil {
		return errUnit{err}
	}
	return nil
}

func connectorRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /connectors", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Connectors(r.Context())
		reply(w, "connectors", http.StatusOK, c, err)
	})
	mux.HandleFunc("POST /connectors/{name}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, "connector-add", http.StatusNoContent, nil, s.AddConnector(r.Context(), r.PathValue("name")))
	})
	mux.HandleFunc("POST /connectors/{name}/verified", func(w http.ResponseWriter, r *http.Request) {
		reply(w, "connector-verified", http.StatusNoContent, nil, s.VerifyConnector(r.Context(), r.PathValue("name")))
	})
	mux.HandleFunc("DELETE /connectors/{name}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, "connector-remove", http.StatusNoContent, nil, s.RemoveConnector(r.Context(), r.PathValue("name")))
	})
}
