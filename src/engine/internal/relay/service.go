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

	// lostKeyProblem is what a claimed box says once it started over because secretd no longer
	// held its secret: the control plane still lists it, under a secret nobody has.
	lostKeyProblem = "This box lost its key: claim it again, and remove its old entry in your account"
)

// Tunnel is the box's way onto the relay while claimed.
type Tunnel interface {
	Start(id Identity, secret string) error
	Stop()
	// Forget wipes what the tunnel side holds for this identity (the box's certificate). Idempotent.
	Forget() error
}

// Status is what the console shows of the claim.
type Status struct {
	State   State  `json:"state"`
	Name    string `json:"name"`
	Address string `json:"address"` // <name>.<box-domain>, when claimed
	Waiting string `json:"waiting"` // "", "relay" or "certificate"
	Problem string `json:"problem"`
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

	op       sync.Mutex // serializes the start, ClaimStart, ClaimFinish and checkStatus
	removed  bool       // a 401 was seen and the start-over is not through; guarded by op
	tunnelUp bool       // the tunnel started and was not stopped since; guarded by op

	mu      sync.Mutex // guards problem and tunnelProblem
	problem string     // the claim's: the status call, secretd, a start-over
	// tunnelProblem is the tunnel's, kept apart so a good status call doesn't hide it; Status
	// shows it first.
	tunnelProblem string
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

// secret is the box's secret, made on first use. Only an unclaimed or claiming box may make one:
// the control plane holds a claimed box's secret's hash (claimedSecret is a claimed box's).
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

// claimedSecret is a claimed box's secret, never made anew. Secretd unreachable changes nothing
// (false, the problem said); secretd holding none starts the box over, as a new one the owner
// claims again (false). Call with op held.
func (s *Service) claimedSecret(ctx context.Context) (string, bool) {
	v, ok, err := s.sec.Get(ctx, secretName)
	if err != nil {
		s.setProblem("secrets unavailable")
		return "", false
	}
	if ok {
		return v, true
	}
	if err := s.startOver(ctx); err != nil { // the next tick finds it absent and tries again
		slog.Error("relay: start over after a lost key", "err", err)
		s.setProblem("couldn't start over after the box lost its key")
		return "", false
	}
	slog.Warn("relay: the box's secret was gone; it started over, unclaimed")
	s.setProblem(lostKeyProblem)
	return "", false
}

// Run starts the tunnel when the box is claimed and checks the claim until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.op.Lock()
	if id, err := s.store.Identity(ctx); err != nil {
		slog.Error("relay: identity", "err", err)
		s.setProblem("couldn't read the box's identity")
	} else if id.State == Claimed {
		if secret, ok := s.claimedSecret(ctx); ok {
			s.startTunnel(id, secret)
		}
	}
	s.op.Unlock()
	ticks := s.ticks
	if ticks == nil {
		t := time.NewTicker(statusInterval)
		defer t.Stop()
		ticks = t.C
	}
	for {
		select {
		case <-ctx.Done():
			s.op.Lock()
			s.stopTunnel()
			s.op.Unlock()
			return
		case <-ticks:
			s.checkStatus(ctx)
		}
	}
}

// startTunnel starts the tunnel with the box's secret, saying so when it can't (the next tick
// tries again). Call with op held.
func (s *Service) startTunnel(id Identity, secret string) {
	if err := s.tunnel.Start(id, secret); err != nil {
		slog.Error("relay: start tunnel", "err", err)
		s.setTunnelProblem("can't start the connection to the relay")
		return
	}
	s.tunnelUp = true
	s.setTunnelProblem("")
}

// stopTunnel stops the tunnel. Call with op held.
func (s *Service) stopTunnel() {
	s.tunnel.Stop()
	s.tunnelUp = false
	s.setTunnelProblem("")
}

func (s *Service) setProblem(p string)       { s.mu.Lock(); s.problem = p; s.mu.Unlock() }
func (s *Service) setTunnelProblem(p string) { s.mu.Lock(); s.tunnelProblem = p; s.mu.Unlock() }

// checkStatus asks the control plane whether the box is still claimed, and starts the tunnel
// again if it isn't running. Only a 401, or secretd holding no secret, changes the claim.
func (s *Service) checkStatus(ctx context.Context) {
	s.op.Lock()
	defer s.op.Unlock()
	id, err := s.store.Identity(ctx)
	if err != nil || id.State != Claimed {
		return
	}
	if s.removed { // retrying a start-over that failed part-way: the secret may be gone already
		s.finishStartOver(ctx)
		return
	}
	secret, ok := s.claimedSecret(ctx)
	if !ok {
		return
	}
	claimed, err := s.cloud.Status(ctx, id.BoxID, secret)
	switch {
	case err != nil:
		slog.Warn("relay: status check failed", "err", err)
		s.setProblem("can't reach the service")
	case !claimed:
		s.removed = true
		s.finishStartOver(ctx)
		return
	default:
		s.setProblem("")
	}
	if !s.tunnelUp {
		s.startTunnel(id, secret)
	}
}

func (s *Service) finishStartOver(ctx context.Context) {
	if err := s.startOver(ctx); err != nil {
		slog.Error("relay: start over", "err", err)
		s.setProblem("couldn't start over after removal")
		return
	}
	s.setProblem("")
}

// startOver makes the box a new one. Every step is idempotent and Reset (which leaves Claimed)
// comes last, so a failure leaves the box Claimed and the next tick tries again. Call with op
// held.
func (s *Service) startOver(ctx context.Context) error {
	s.stopTunnel()
	if err := s.tunnel.Forget(); err != nil {
		return err
	}
	if err := s.sec.Delete(ctx, secretName); err != nil {
		return err
	}
	if err := s.store.Reset(ctx); err != nil {
		return err
	}
	s.removed = false                        // unclaimed from here on
	if _, err := s.secret(ctx); err != nil { // a failed mint is ClaimStart's to redo
		slog.Warn("relay: make the new box's secret", "err", err)
	}
	return nil
}

// ClaimStart has the control plane mail a code to email.
func (s *Service) ClaimStart(ctx context.Context, email string) error {
	s.op.Lock()
	defer s.op.Unlock()
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

var (
	errNotClaiming = errors.New("no claim in progress")
	errNotClaimed  = errors.New("box not claimed")
)

// ClaimFinish claims the box with the mailed code and starts the tunnel.
func (s *Service) ClaimFinish(ctx context.Context, code string) error {
	s.op.Lock()
	defer s.op.Unlock()
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
	s.removed = false
	s.setProblem("")
	s.startTunnel(id, secret)
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
	if s.tunnelProblem != "" {
		st.Problem = s.tunnelProblem
	}
	s.mu.Unlock()
	if id.State == Claimed {
		st.Name, st.Address = id.Name, id.Name+"."+id.BoxDomain
		st.Waiting = s.probe()
	}
	return st
}

// AddDevice records a device certificate the CA issued, while the box is claimed (a box starting
// over drops its devices, so none is added past that).
func (s *Service) AddDevice(ctx context.Context, serial, label string, expires time.Time) error {
	s.op.Lock()
	defer s.op.Unlock()
	id, err := s.store.Identity(ctx)
	if err != nil {
		return err
	}
	if id.State != Claimed {
		return errNotClaimed
	}
	return s.store.AddDevice(ctx, Device{Serial: serial, Label: label, Issued: s.now(), Expires: expires})
}

// Devices are the device certificates the box issued, oldest first.
func (s *Service) Devices(ctx context.Context) ([]Device, error) { return s.store.Devices(ctx) }

// RemoveDevice drops a device certificate from the active list. Idempotent.
func (s *Service) RemoveDevice(ctx context.Context, serial string) error {
	return s.store.RemoveDevice(ctx, serial)
}
