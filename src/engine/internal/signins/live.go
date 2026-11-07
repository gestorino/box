package signins

import (
	"context"
	"errors"
	"time"
)

// ErrNotWaiting refuses Done and Give up on a site whose run is not waiting for the owner to solve a
// captcha. A code's wait is open to the owner's hands on the run's screen too (the worker switches
// the screen's input), but ends when the code comes, not by Done.
var ErrNotWaiting = errors.New("no run on this site is waiting for you")

// captchaWait is how long a run waits for the owner to solve a captcha. The worker's human_check
// outlasts it (OWNER_TIMEOUT in agent.py).
const captchaWait = 10 * time.Minute

// What a run can wait on with the owner's hands on its page, as the console reads it (RunDetail.wait).
const (
	waitCaptcha = "captcha"
	waitCode    = "code"
)

// ownerWait is one open wait in which the owner may act on the page: a captcha's, whose answer takes
// Done (true) or Give up (false) once, or an SMS code's, which nobody answers: it ends when the code
// comes or its time is up.
type ownerWait struct {
	until  time.Time
	answer chan bool
	kind   string
}

// Done is the owner's "solved it": the run's ask answers "done" and the model looks again.
func (s *Service) Done(site string) error { return s.endOwnerWait(site, true) }

// GiveUp answers the run's ask no: the run ends at warn, "the captcha wasn't solved".
func (s *Service) GiveUp(site string) error { return s.endOwnerWait(site, false) }

func (s *Service) endOwnerWait(site string, done bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.waits[site]
	if w == nil || w.kind == waitCode {
		return ErrNotWaiting
	}
	// Taken out and answered under the lock, so only one answer is ever sent on the wait's one-slot
	// channel (it never blocks), and a wait no longer in the map always has its answer waiting.
	delete(s.waits, site)
	w.answer <- done
	return nil
}

// closeWait takes the wait out for its waiter, false when Done or Give up took it first: its answer
// is then on the channel already.
func (s *Service) closeWait(site string, w *ownerWait) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waits[site] != w {
		return false
	}
	delete(s.waits, site)
	return true
}

// waitForOwner holds a run's human_check until the owner presses Done or Give up, captchaWait passes,
// or ctx ends (the worker went away, the engine is stopping). Meanwhile the site shows waiting with its
// deadline, and the worker has the run's screen open to the owner. An answer the owner was told was
// taken counts, even one that landed as the wait ended. The run log says that it waited and how it
// ended, never what the owner did in the page.
func (s *Service) waitForOwner(ctx context.Context, site string, log *runLog, begun func()) (solved bool) {
	w := &ownerWait{until: s.cfg.Now().Add(captchaWait), answer: make(chan bool, 1), kind: waitCaptcha}
	defer s.openWait(site, w, phaseWaiting)()
	log.box("Waiting for you to solve a captcha")
	if begun != nil {
		begun()
	}
	var done bool
	select {
	case done = <-w.answer:
	case <-s.cfg.After(captchaWait):
		if s.closeWait(site, w) {
			log.box("Nobody came in 10 minutes")
			return false
		}
		done = <-w.answer
	case <-ctx.Done():
		if s.closeWait(site, w) {
			return false
		}
		done = <-w.answer
	}
	if !done {
		log.box("You gave up")
		return false
	}
	log.box("You solved it")
	return true
}

// openWait puts the site's run in the wait's phase and returns what ends it: the run's own phase
// back. A wait Done or Give up took out is already gone.
func (s *Service) openWait(site string, w *ownerWait, phase string) (end func()) {
	s.mu.Lock()
	prev := s.phases[site]
	s.phases[site] = phase
	s.waits[site] = w
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.waits[site] == w {
			delete(s.waits, site)
		}
		s.phases[site] = prev
	}
}

// running is what the site list shows of a run in flight: its phase, the deadline of a wait for the
// owner, and the run in flight (0 when the operation is not a run).
func (s *Service) running(site string) (phase string, waitingUntil *time.Time, run int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.waits[site]; w != nil {
		until := w.until
		waitingUntil = &until
	}
	return s.phases[site], waitingUntil, s.current[site]
}

// waitOf is what the run waits for (waitCaptcha or waitCode), and until when: only while
// it is the run in flight on the site and its wait is open. "" otherwise.
func (s *Service) waitOf(site string, run int64) (wait string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.waits[site]
	if w == nil || s.current[site] != run {
		return "", time.Time{}
	}
	return w.kind, w.until
}

// humanCheck answers the worker's human_check: the owner solves it on the run's screen, Done or Give
// up (begun, when set, is called once the wait is open). ok is false when they gave up, nobody came, or ctx ended.
func (s *Service) humanCheck(ctx context.Context, site string, log *runLog, begun func()) (string, bool) {
	if s.waitForOwner(ctx, site, log, begun) {
		return "done", true
	}
	return "", false
}
