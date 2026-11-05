package email

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"concierge/engine/internal/secrets"
)

// secretName is the app password in emaild's namespace of secretd.
const secretName = "app-password"

// Status: State is disconnected, connecting, copying, paused_mobile, connected or auth_failed.
// Copied counts the account's stored emails while the first copy runs.
type Status struct {
	State   string `json:"state"`
	Address string `json:"address,omitempty"`
	Copied  int    `json:"copied,omitempty"`
	Error   string `json:"error,omitempty"`
}

const (
	batch      = 10 // messages per fetch: bounded memory with 25 MB emails
	sweep      = 5 * time.Minute
	recheck    = time.Minute
	backoffMin = 10 * time.Second
	backoffMax = 5 * time.Minute
	purgeEvery = time.Hour
)

// Service follows one mailbox: the first copy (within the retention, held off mobile data unless
// allowed), then new mail as it arrives, and the purge. A pass (syncOnce) runs under work, as do
// Connect and Disconnect, so they never interleave. The read state (read.go) has its own loop and
// its own short connection, so it never waits on a pass.
type Service struct {
	dial      Dial
	send      Send
	reconcile Reconcile
	secrets   *secrets.Client
	a         *Archive
	retention func() (days int, known bool)
	uplink    func(context.Context) (string, error)
	now       func() time.Time
	wake      chan struct{}
	nudge     chan struct{} // the owner opened a thread: reconcile now

	work      sync.Mutex
	lastPurge time.Time
	lastDays  int

	mu     sync.Mutex
	acct   *Account // the account followed, nil when none; its Cursor changes only under work
	sess   Session
	status Status
	// checking reports a setup check waiting for its reply (SetChecking); nil: none.
	checking func() bool
}

func NewService(dial Dial, send Send, reconcile Reconcile, sec *secrets.Client, a *Archive, retention func() (int, bool), uplink func(context.Context) (string, error), now func() time.Time) *Service {
	return &Service{dial: dial, send: send, reconcile: reconcile, secrets: sec, a: a, retention: retention, uplink: uplink, now: now,
		wake: make(chan struct{}, 1), nudge: make(chan struct{}, 1), lastDays: -1, status: Status{State: "disconnected"}}
}

// Start picks up the account the box followed before it restarted, then follows it until ctx ends.
func (s *Service) Start(ctx context.Context) error {
	if err := s.resume(ctx); err != nil {
		return err
	}
	go s.run(ctx)
	go s.reconcileLoop(ctx)
	return nil
}

func (s *Service) resume(ctx context.Context) error {
	acct, err := s.a.Current(ctx)
	if err != nil || acct == nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case acct.State == "auth_failed":
		s.status = Status{State: "auth_failed", Address: acct.Address}
	case acct.State == "disconnected":
		s.status = Status{State: "disconnected", Address: acct.Address}
	default:
		s.acct, s.status = acct, Status{State: "connecting", Address: acct.Address}
	}
	return nil
}

// password reads the app password fresh from secretd. A secretd that cannot answer is
// secrets.ErrUnavailable, logged without any value; it is not the password's fault, so no state
// changes on it. A password secretd doesn't hold is a refused login, ErrAuth, as Google refusing it.
func (s *Service) password(ctx context.Context) (string, error) {
	v, ok, err := s.secrets.Get(ctx, secretName)
	if err != nil {
		slog.Error("email: secrets unavailable", "err", err)
		return "", err
	}
	if !ok {
		slog.Error("email: the app password is not in secretd")
		return "", ErrAuth
	}
	return v, nil
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Connect checks the password with the server before saving anything. An address the box has read
// before resumes from where it stopped; otherwise history is "copy" (what is in the mailbox, within
// the retention) or "from_now".
func (s *Service) Connect(ctx context.Context, address, password, history string, copyOnMobile bool) error {
	s.work.Lock()
	defer s.work.Unlock()
	if st := s.Status().State; st != "disconnected" && st != "auth_failed" {
		return ErrState
	}
	sess, err := s.dial(ctx, address, password)
	if err != nil {
		return err
	}
	if err := s.secrets.Put(ctx, secretName, password); err != nil {
		sess.Close()
		return err
	}
	acct := &Account{Address: address, CopyOnMobile: copyOnMobile, State: "connected"}
	prev, err := s.a.Account(ctx, address)
	switch {
	case err != nil:
		sess.Close()
		return err
	case prev != nil && prev.UIDValidity != 0:
		acct.Cursor = prev.Cursor
	default:
		next := sess.UIDNext() - 1
		acct.Cursor = Cursor{UIDValidity: sess.UIDValidity(), LastUID: next}
		if history == "copy" {
			acct.CopyUntilUID = next
		}
	}
	if err := s.a.PutAccount(ctx, *acct); err != nil {
		sess.Close()
		return err
	}
	s.mu.Lock()
	s.acct, s.sess, s.status = acct, sess, Status{State: "connecting", Address: address}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

// Disconnect stops following the account and deletes its password from secretd; deleteEmails also deletes what
// the box kept of it.
func (s *Service) Disconnect(ctx context.Context, deleteEmails bool) error {
	s.mu.Lock()
	disconnected := s.status.State == "disconnected"
	s.mu.Unlock()
	if disconnected {
		return ErrState
	}
	// Before the session is taken: nothing changes if secretd can't delete the password.
	if err := s.secrets.Delete(ctx, secretName); err != nil {
		slog.Error("email: deleting the app password", "err", err)
		return err
	}
	s.mu.Lock()
	st, sess := s.status, s.sess // read and cleared together, so a session dialled meanwhile is not missed
	s.acct, s.sess = nil, nil
	s.mu.Unlock()
	if sess != nil {
		sess.Close() // ends a Wait or a fetch in flight
	}
	s.work.Lock()
	defer s.work.Unlock()
	if err := s.a.Forget(ctx, st.Address, deleteEmails); err != nil {
		return err
	}
	next := Status{State: "disconnected", Address: st.Address}
	if deleteEmails {
		next.Address = ""
	}
	s.mu.Lock()
	s.status = next
	s.mu.Unlock()
	return nil
}

// Send sends m from the account the box follows, with its app password from secretd, under a Message-ID it
// picks and returns; ErrState when it follows none. The sent email reaches the archive through All
// Mail, so the next pass is woken for it.
func (s *Service) Send(ctx context.Context, m Outgoing) (string, error) {
	s.mu.Lock()
	acct := s.acct
	s.mu.Unlock()
	if acct == nil {
		return "", ErrState
	}
	password, err := s.password(ctx)
	if err != nil {
		return "", err
	}
	m.MessageID = rand.Text() + "@concierge"
	if err := s.send(ctx, acct.Address, password, m); err != nil {
		return "", err
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return m.MessageID, nil
}

// SetChecking tells the first copy whether a setup check waits for its reply: while one does, the
// copy yields after each batch, so the next pass fetches new mail (the reply) first.
func (s *Service) SetChecking(checking func() bool) {
	s.mu.Lock()
	s.checking = checking
	s.mu.Unlock()
}

func (s *Service) waitingForCheck() bool {
	s.mu.Lock()
	checking := s.checking
	s.mu.Unlock()
	return checking != nil && checking()
}

// Remove is the owner removing Email: it works in any state. It deletes the app password from
// secretd (secretd down: ErrUnavailable and nothing changes — never past a password left behind),
// stops following the account and disconnects every account; deleteData also wipes every account
// and every email. There is nothing to log out of — an app password is only revoked at Google.
func (s *Service) Remove(ctx context.Context, deleteData bool) error {
	if err := s.secrets.Delete(ctx, secretName); err != nil {
		slog.Error("email: deleting the app password", "err", err)
		return err
	}
	s.mu.Lock()
	st, sess := s.status, s.sess
	s.acct, s.sess = nil, nil
	s.mu.Unlock()
	if sess != nil {
		sess.Close() // ends a Wait or a fetch in flight
	}
	s.work.Lock()
	defer s.work.Unlock()
	next := Status{State: "disconnected", Address: st.Address}
	var err error
	if deleteData {
		err, next.Address = s.a.Wipe(ctx), ""
	} else {
		err = s.a.ForgetPasswords(ctx)
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.status = next
	s.mu.Unlock()
	return nil
}

// syncOnce is one pass: the purge, then — when an account is followed — the first copy's next
// stretch and the new mail.
func (s *Service) syncOnce(ctx context.Context) error {
	s.work.Lock()
	defer s.work.Unlock()
	s.purge(ctx)
	s.mu.Lock()
	acct, sess := s.acct, s.sess
	s.mu.Unlock()
	if acct == nil {
		return nil
	}
	if sess == nil {
		password, err := s.password(ctx)
		if err == nil {
			sess, err = s.dial(ctx, acct.Address, password)
		}
		if errors.Is(err, ErrAuth) {
			// A refused login is not retried: hammering it gets the account locked.
			if err := s.a.SetState(ctx, acct.Address, "auth_failed"); err != nil {
				slog.Error("email: persisting auth_failed", "err", err)
			}
			s.mu.Lock()
			if s.acct == acct {
				s.acct, s.status = nil, Status{State: "auth_failed", Address: acct.Address}
			}
			s.mu.Unlock()
			return err
		} else if err != nil {
			return err
		}
		s.mu.Lock()
		if s.acct != acct { // disconnected while dialling: this session must not outlive the request
			s.mu.Unlock()
			sess.Close()
			return nil
		}
		s.sess = sess
		s.mu.Unlock()
	}
	if err := s.pass(ctx, acct, sess); err != nil {
		sess.Close()
		s.mu.Lock()
		if s.sess == sess {
			s.sess = nil
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *Service) pass(ctx context.Context, acct *Account, sess Session) error {
	if uv := sess.UIDValidity(); uv != acct.UIDValidity { // the server renumbered: copy the window again
		next := sess.UIDNext() - 1
		acct.Cursor = Cursor{UIDValidity: uv, LastUID: next, CopyUntilUID: next}
		if err := s.a.Renumber(ctx, acct.Address, acct.Cursor); err != nil {
			return err
		}
	}
	// New mail first: it is small and must keep arriving whatever happens to the first copy (paused
	// on mobile data, or stopped by Gmail's daily download cap).
	uids, err := sess.UIDsAfter(ctx, acct.LastUID)
	if err != nil {
		return err
	}
	if err := s.fetch(ctx, acct, sess, uids, func(c *Cursor, uid uint32) { c.LastUID = uid }); err != nil {
		return err
	}
	state := "connected"
	if acct.Copying() {
		if state, err = s.copy(ctx, acct, sess); err != nil {
			return err
		}
	}
	s.setState(ctx, acct, state)
	return nil
}

// copy runs the first copy as far as it may go this pass, and says which state that leaves.
func (s *Service) copy(ctx context.Context, acct *Account, sess Session) (string, error) {
	days, known := s.retention()
	if !known { // the window depends on it: wait rather than guess
		return "copying", nil
	}
	var day time.Time
	if days > 0 {
		day = s.now().AddDate(0, 0, -days)
	}
	all, err := sess.UIDsSince(ctx, day)
	if err != nil {
		return "", err
	}
	var uids []uint32
	for _, u := range all {
		if u > acct.CopiedUID && u <= acct.CopyUntilUID {
			uids = append(uids, u)
		}
	}
	slices.Sort(uids)
	for len(uids) > 0 {
		// Asked before every batch: the box can fall back to the SIM in the middle of a copy.
		if !acct.CopyOnMobile {
			if up, err := s.uplink(ctx); err != nil || up == "mobile" {
				return "paused_mobile", nil
			}
		}
		part := uids[:min(batch, len(uids))]
		uids = uids[len(part):]
		if err := s.fetch(ctx, acct, sess, part, func(c *Cursor, uid uint32) { c.CopiedUID = uid }); err != nil {
			return "", err
		}
		if len(uids) > 0 && s.waitingForCheck() {
			return "copying", nil
		}
	}
	acct.CopiedUID = acct.CopyUntilUID // nothing left inside the window
	return "connected", s.a.SetCursor(ctx, acct.Address, acct.Cursor)
}

// fetch stores uids in batches, oldest first, moving the cursor only past what is stored.
func (s *Service) fetch(ctx context.Context, acct *Account, sess Session, uids []uint32, advance func(*Cursor, uint32)) error {
	slices.Sort(uids)
	for len(uids) > 0 {
		part := uids[:min(batch, len(uids))]
		uids = uids[len(part):]
		msgs, err := sess.Fetch(ctx, part)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if m.Draft {
				continue
			}
			if err := s.a.Save(ctx, acct.Address, m.UID, m.Date, m.Raw); err != nil {
				return err
			}
		}
		advance(&acct.Cursor, part[len(part)-1])
		if err := s.a.SetCursor(ctx, acct.Address, acct.Cursor); err != nil {
			return err
		}
		if acct.Copying() {
			s.setState(ctx, acct, "copying")
		}
	}
	return nil
}

func (s *Service) setState(ctx context.Context, acct *Account, state string) {
	st := Status{State: state, Address: acct.Address}
	if state == "copying" || state == "paused_mobile" {
		st.Copied, _ = s.a.Count(ctx, acct.Address)
	}
	s.mu.Lock()
	if s.acct == acct {
		s.status = st
	}
	s.mu.Unlock()
}

// purge runs hourly and as soon as the retention changes, once hostd has answered.
func (s *Service) purge(ctx context.Context) {
	days, known := s.retention()
	if !known || days == s.lastDays && s.now().Sub(s.lastPurge) < purgeEvery {
		return
	}
	if err := s.a.Purge(ctx, days); err != nil {
		slog.Error("email: purge", "err", err)
		return
	}
	s.lastDays, s.lastPurge = days, s.now()
}

// run passes until ctx ends: after a good pass on a connected mailbox it waits on the server for new
// mail (at most sweep); otherwise it waits a minute, a backoff after a failure, or for Connect.
func (s *Service) run(ctx context.Context) {
	backoff := backoffMin
	for {
		err := s.syncOnce(ctx)
		wait := recheck
		if err != nil && !errors.Is(err, ErrAuth) {
			if ctx.Err() == nil {
				slog.Warn("email: mailbox unavailable, will retry", "in", backoff, "err", err)
			}
			wait, backoff = backoff, min(2*backoff, backoffMax)
		} else {
			backoff = backoffMin
		}
		s.mu.Lock()
		sess, state := s.sess, s.status.State
		s.mu.Unlock()
		if err == nil && sess != nil && state == "connected" {
			if err := sess.Wait(ctx, sweep); err != nil && ctx.Err() == nil {
				slog.Debug("email: waiting for new mail", "err", err)
			}
			if ctx.Err() != nil {
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-s.wake:
		}
	}
}
