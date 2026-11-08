package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"sync"
	"time"

	"concierge/engine/internal/secrets"
)

const (
	secretName     = "box-secret"
	statusInterval = 10 * time.Minute
)

// Tunnel is the box's way onto the relay while claimed.
type Tunnel interface {
	Start(id Identity, secret string) error
	Stop()
}

// Status is what the console shows of the claim.
type Status struct {
	State   State
	Name    string
	Address string // <name>.<box-domain>, when claimed
	Waiting string // "", "relay" or "certificate"
	Problem string
}

// Service is the box's claim: the identity, the secret, the tunnel while claimed, and the
// status loop that notices the owner removed the box.
type Service struct {
	store  *Store
	cloud  *Cloud
	sec    *secrets.Client
	tunnel Tunnel
	probe  func() string
	now    func() time.Time
	ticks  <-chan time.Time // the status loop's clock; nil means every statusInterval

	mu      sync.Mutex
	problem string
}

func NewService(store *Store, cloud *Cloud, sec *secrets.Client, tunnel Tunnel, probe func() string, now func() time.Time) *Service {
	return &Service{store: store, cloud: cloud, sec: sec, tunnel: tunnel, probe: probe, now: now}
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// secret is the box's secret, made on first use.
func (s *Service) secret(ctx context.Context) (string, error) {
	v, ok, err := s.sec.Get(ctx, secretName)
	if err != nil {
		return "", err
	}
	if ok {
		return v, nil
	}
	if v, err = newSecret(); err != nil {
		return "", err
	}
	return v, s.sec.Put(ctx, secretName, v)
}

// Run starts the tunnel when the box is claimed and checks the claim until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if id, err := s.store.Identity(ctx); err != nil {
		slog.Error("relay: identity", "err", err)
	} else if id.State == Claimed {
		s.startTunnel(ctx, id)
	}
	ticks := s.ticks
	if ticks == nil {
		t := time.NewTicker(statusInterval)
		defer t.Stop()
		ticks = t.C
	}
	for {
		select {
		case <-ctx.Done():
			s.tunnel.Stop()
			return
		case <-ticks:
			s.checkStatus(ctx)
		}
	}
}

func (s *Service) startTunnel(ctx context.Context, id Identity) {
	secret, err := s.secret(ctx)
	if err == nil {
		err = s.tunnel.Start(id, secret)
	}
	if err != nil {
		slog.Error("relay: start tunnel", "err", err)
		s.setProblem("can't start the connection to the relay")
	}
}

func (s *Service) setProblem(p string) { s.mu.Lock(); s.problem = p; s.mu.Unlock() }

// checkStatus asks the control plane whether the box is still claimed. Only a 401 changes anything.
func (s *Service) checkStatus(ctx context.Context) {
	id, err := s.store.Identity(ctx)
	if err != nil || id.State != Claimed {
		return
	}
	secret, err := s.secret(ctx)
	if err != nil {
		s.setProblem("secrets unavailable")
		return
	}
	claimed, err := s.cloud.Status(ctx, id.BoxID, secret)
	if err != nil {
		slog.Warn("relay: status check failed", "err", err)
		s.setProblem("can't reach the service")
		return
	}
	s.setProblem("")
	if claimed {
		return
	}
	s.tunnel.Stop()
	if err := s.startOver(ctx); err != nil {
		slog.Error("relay: start over", "err", err)
		s.setProblem("couldn't start over after removal")
	}
}

// startOver makes the box a new one: a new box_id and a new secret, unclaimed.
func (s *Service) startOver(ctx context.Context) error {
	if err := s.store.Reset(ctx); err != nil {
		return err
	}
	if err := s.sec.Delete(ctx, secretName); err != nil {
		return err
	}
	_, err := s.secret(ctx)
	return err
}

// ClaimStart has the control plane mail a code to email.
func (s *Service) ClaimStart(ctx context.Context, email string) error {
	if _, err := s.secret(ctx); err != nil {
		return err
	}
	id, err := s.store.Identity(ctx)
	if err != nil {
		return err
	}
	if id.State == Claimed {
		return ErrAlreadyClaimed
	}
	if err := s.cloud.ClaimStart(ctx, email); err != nil {
		return err
	}
	id.State, id.Email = Claiming, email
	return s.store.SetIdentity(ctx, id)
}

var errNotClaiming = errors.New("no claim in progress")

// ClaimFinish claims the box with the mailed code and starts the tunnel.
func (s *Service) ClaimFinish(ctx context.Context, code string) error {
	id, err := s.store.Identity(ctx)
	if err != nil {
		return err
	}
	if id.State != Claiming {
		return errNotClaiming
	}
	secret, err := s.secret(ctx)
	if err != nil {
		return err
	}
	res, err := s.cloud.ClaimFinish(ctx, id.Email, code, id.BoxID, secret)
	if err != nil {
		return err
	}
	id.State, id.Name, id.BoxDomain, id.Relay = Claimed, res.Name, res.BoxDomain, res.Relay
	if err := s.store.SetIdentity(ctx, id); err != nil {
		return err
	}
	s.setProblem("")
	s.startTunnel(ctx, id)
	return nil
}

// Status is the claim as the console shows it.
func (s *Service) Status() Status {
	id, err := s.store.Identity(context.Background())
	if err != nil {
		return Status{Problem: "couldn't read the box's identity"}
	}
	s.mu.Lock()
	st := Status{State: id.State, Problem: s.problem}
	s.mu.Unlock()
	if id.State == Claimed {
		st.Name, st.Address = id.Name, id.Name+"."+id.BoxDomain
		st.Waiting = s.probe()
	}
	return st
}
