package signins

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"concierge/engine/internal/browser"
)

// An errand is the owner's link and what to do there: an agent run (agent.go) of kind "errand", asking
// the owner every choice the site's flow presents and Confirm before the press that finally does it.
// Design: docs/superpowers/specs/2026-11-03-errands-design.md.

// ErrBadErrand refuses an errand without an http(s) link or something to do.
var ErrBadErrand = errors.New("an errand needs an http(s) link and what to do there")

// errandSite is where an errand's link goes: the site's key, and with the box's registered account
// on it, its profile (the key) and the values it signs in with. Without one the errand runs in a
// throwaway profile with no values. The browser may go anywhere on the key's domain.
func (s *Service) errandSite(ctx context.Context, host string) (key, session string, domains []string, values map[string]string, err error) {
	key, err = siteKey(host)
	if err != nil || !isKey(key) {
		return "", "", nil, nil, ErrBadErrand
	}
	domains = []string{key, "*." + key}
	a, has, err := s.cfg.Store.Account(ctx, key)
	if err != nil {
		return "", "", nil, nil, err
	}
	if !has || a.State != "registered" {
		return key, "", domains, nil, nil
	}
	password, has, err := s.cfg.Secrets.Get(ctx, passwordName(key))
	if err != nil {
		return "", "", nil, nil, err
	}
	if !has {
		return "", "", nil, nil, ErrNoPassword
	}
	return key, key, domains, map[string]string{"email": a.Email, "secret:password": password}, nil
}

// ErrNoPassword is a registered account whose password secretd doesn't keep: never an errand that
// signs in with nothing. The owner removes the account and registers again.
var ErrNoPassword = errors.New("the box's password for this site is missing — remove the account and register again")

// Errand is what the owner starts: the link, what to do there, and the action's effect on the site's
// appointments (none when ""), with the appointment a change or a cancel is about (0 none).
type Errand struct {
	URL, Action string
	Effect      string
	Appointment int64
}

// StartErrand starts the owner's errand.
func (s *Service) StartErrand(ctx context.Context, e Errand) (int64, error) {
	effect := cmp.Or(e.Effect, "none")
	if !effects[effect] {
		return 0, fmt.Errorf("%w: an effect is none, create, change, cancel or sync", ErrBadErrand)
	}
	return s.errand(ctx, e, effect)
}

// errand starts an errand of effect, check included (CheckAppointment's own); its end applies the
// effect to the site's appointments.
func (s *Service) errand(ctx context.Context, e Errand, effect string) (int64, error) {
	u, err := url.Parse(strings.TrimSpace(e.URL))
	action := strings.TrimSpace(e.Action)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || action == "" {
		return 0, ErrBadErrand
	}
	if s.cfg.Base.Err() != nil {
		return 0, ErrShuttingDown
	}
	if err := s.keyed(ctx); err != nil {
		return 0, err
	}
	site, session, domains, values, err := s.errandSite(ctx, u.Hostname())
	if err != nil {
		return 0, err
	}
	if err := s.errandEffect(ctx, site, effect, e.Appointment); err != nil {
		return 0, err
	}
	if !s.claim(site, phaseErrand) {
		return 0, ErrBusy
	}
	return s.launch(ctx, agentStart{key: site, kind: "errand", url: u.String(), action: action, session: session,
		domains: domains, values: values, email: values["email"], done: "done", notDone: "not done", record: recordOf(effect),
		finish: func(ctx context.Context, run int64, reply browser.Message, err error, ended string) func() {
			if err == nil && ended == "" && ctx.Err() == nil {
				s.applyEffect(ctx, site, run, effect, e.Appointment, session != "", reply)
			}
			return nil
		}})
}
