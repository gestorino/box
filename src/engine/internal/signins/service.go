// Package signins owns the box's catalog of sites, its own accounts on them, and the runs that work
// on a site in its browser: agent runs (agent.go) of each kind — errands, registrations, sign-in checks.
// Design: docs/superpowers/specs/2026-11-03-registration-as-errand-design.md.
package signins

import (
	"context"
	"errors"
	"sync"
	"time"

	"concierge/engine/internal/browser"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/secrets"
)

// Runner is the part of the browser worker the service needs.
type Runner interface {
	Request(ctx context.Context, typ string, body browser.Message) (browser.Message, error)
	RequestAsk(ctx context.Context, typ string, body browser.Message, ask browser.Ask, progress browser.Progress) (browser.Message, error)
}

type Config struct {
	Store  *Store
	Worker Runner
	// Secrets is the engine's secretd: each account's password, signins/<key>/password.
	Secrets *secrets.Client
	SMS     listen.Listener // smsd: where SMS codes arrive
	Mail    Mailbox         // emaild: the mailbox the box's plus-addresses belong to
	Inbox   listen.Listener // emaild: where the emails a site sends an account arrive
	Uplink  UplinkReader    // hostd: runs wait off mobile data
	Owner   OwnerReader     // hostd: the Account's names and phone number (the box's SIM)
	// ScreensDir holds each run's VNC screen, one directory per site, the worker's (read only here);
	// empty serves no screens: this computer has none.
	ScreensDir string
	// RecordingsDir is where the worker keeps each run's video and log, <dir>/<site>/<run>/ (runDir;
	// read only here: the worker deletes what the engine asks it to); empty records nothing.
	RecordingsDir string
	// NoVNCDir is noVNC's files (the box's novnc package, /usr/share/novnc), served for the screens.
	NoVNCDir string
	Now      func() time.Time // defaults to time.Now
	// After is the timer of a wait for the owner (a captcha, a question); defaults to time.After.
	After func(time.Duration) <-chan time.Time
	// Base is what background runs (an errand outlives its HTTP request) run under; cancel it to
	// stop them. Defaults to context.Background().
	Base context.Context
	// HasKey says the OpenRouter key is stored: every run's model needs it, so none starts without
	// it, nor while it can't be read (secretd unavailable). Nil is no key.
	HasKey func(context.Context) (bool, error)
}

type Service struct {
	cfg Config
	// codes is how long a run waits for an SMS code or an email.
	codes time.Duration
	// caughtUp is how long a run waits for smsd to say its SMS listener is listening (caughtUpWait).
	caughtUp time.Duration

	// phases holds the sites an operation is working on, and what it is doing: one browser run per
	// site at a time, because the site's profile is one Chromium-locked directory and two runs would
	// race on one account. The phase lives here, not in the account row, so an engine killed mid-run
	// leaves only terminal states behind.
	mu     sync.Mutex
	phases map[string]string
	// waits holds the sites whose run waits for the owner to solve a captcha (phase waiting) or for an
	// SMS code (phase waiting_code), either open to the owner's hands; current the id of the run in
	// flight on each site (set by startRun): its screen is the owner's to see while it holds the site.
	waits   map[string]*ownerWait
	current map[string]int64
	// agents holds each agent run in flight by run id: its stop, and the question it waits on.
	agents map[int64]*agentRun
	// runs counts the background runs in flight.
	runs sync.WaitGroup
}

// ErrShuttingDown refuses starting a background run once Config.Base is done: Wait must be able to
// see the last run start.
var ErrShuttingDown = errors.New("the engine is shutting down")

// ErrNoKey refuses starting a run while no OpenRouter key is stored: it would fail at its first
// model call.
var ErrNoKey = errors.New("no OpenRouter key")

// keyed is the gate before every run: ErrNoKey without the OpenRouter key, or why it can't be read.
func (s *Service) keyed(ctx context.Context) error {
	if s.cfg.HasKey == nil {
		return ErrNoKey
	}
	has, err := s.cfg.HasKey(ctx)
	if err != nil {
		return err
	}
	if !has {
		return ErrNoKey
	}
	return nil
}

// passwordName is the account's password in secretd.
func passwordName(key string) string { return "signins/" + key + "/password" }

// Wait blocks until the background runs in flight have ended; cancel Config.Base first, or it
// waits out an errand.
func (s *Service) Wait() { s.runs.Wait() }

func NewService(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Base == nil {
		cfg.Base = context.Background()
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
	return &Service{cfg: cfg, codes: codeTimeout, caughtUp: caughtUpWait, phases: map[string]string{},
		waits: map[string]*ownerWait{}, current: map[string]int64{}, agents: map[int64]*agentRun{}}
}

// ErrBusy refuses starting a run on a site another run is working on.
var ErrBusy = errors.New("the box is already working on this site")

// The phases an operation holds a site in. Sites shows them in place of the stored state; removing
// is over within the request. Waiting is a run held for the owner to solve a captcha, waiting_code
// one held for an SMS code; the run's own phase comes back when the wait ends.
const (
	phaseRegistering = "registering"
	phaseErrand      = "errand"
	phaseChecking    = "checking"
	phaseWaiting     = "waiting"
	phaseWaitingCode = "waiting_code"
	phaseRemoving    = "removing"
)

// claim marks the site busy in the phase; false when it already is.
func (s *Service) claim(site, phase string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phases[site] != "" {
		return false
	}
	s.phases[site] = phase
	return true
}

// phase is what an operation is doing on the site, "" when none is.
func (s *Service) phase(site string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phases[site]
}

// release ends the site's operation, and with it the run in flight.
func (s *Service) release(site string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.phases, site)
	delete(s.current, site)
}
