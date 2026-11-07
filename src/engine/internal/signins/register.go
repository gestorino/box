package signins

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"concierge/engine/internal/browser"
	listenv1 "concierge/engine/internal/gen/listen/v1"
)

// registerAction is a registration's action: the errand's one specific part.
const registerAction = "create an account"

// ErrAccountExists refuses registering on a site the box already has an account on.
var ErrAccountExists = errors.New("the box already has an account on this site")

// Register is the owner's Register on a card: an errand that creates the box's account on the site,
// starting at the card's link, filling the box's identity (a fresh plus-address, the Account's names,
// the box SIM, a generated password). The account is written before the run starts (warn, "the
// registration was interrupted"), so an engine killed mid-run leaves that honest state. Pressing
// Register is the owner's approval for this one account (P8); the errand still asks "Confirm: …?"
// before the press that creates it.
func (s *Service) Register(ctx context.Context, key string) (int64, error) {
	if s.cfg.Base.Err() != nil {
		return 0, ErrShuttingDown
	}
	if err := s.keyed(ctx); err != nil {
		return 0, err
	}
	c, found, err := s.cfg.Store.Card(ctx, key)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrUnknownSite
	}
	if a, has, err := s.cfg.Store.Account(ctx, key); err != nil {
		return 0, err
	} else if has && a.State == "registered" {
		return 0, ErrAccountExists
	}
	mailbox, owner, err := s.identity(ctx)
	if err != nil {
		return 0, err
	}
	if err := s.offMobile(ctx); err != nil {
		return 0, err
	}
	if !s.claim(key, phaseRegistering) {
		return 0, ErrBusy
	}
	// Again under the claim: a registration that ended between the look above and the claim may have
	// made the account meanwhile.
	if a, has, err := s.cfg.Store.Account(ctx, key); err != nil || (has && a.State == "registered") {
		s.release(key)
		if err != nil {
			return 0, err
		}
		return 0, ErrAccountExists
	}
	values, a, err := s.startAccount(ctx, key, mailbox, owner)
	if err != nil {
		s.release(key)
		return 0, err
	}
	return s.launch(ctx, agentStart{key: key, kind: "register", url: c.Link, action: registerAction, session: key,
		domains: []string{key, "*." + key}, values: values, email: a.Email, done: "registered", notDone: "not registered",
		finish: func(ctx context.Context, run int64, reply browser.Message, err error, ended string) func() {
			return s.registrationEnded(ctx, a, run, reply, err, ended)
		}})
}

// identity is who the box registers as: the Email connector's mailbox, and the Account's names and
// phone (the box's SIM). What is missing refuses the registration before anything is written.
func (s *Service) identity(ctx context.Context) (string, Owner, error) {
	var missing []string
	mailbox, err := s.cfg.Mail.Address(ctx)
	if err != nil {
		slog.Warn("signins: the Email connector did not answer", "err", err)
	}
	if !isAddress(mailbox) {
		missing = append(missing, "email")
	}
	owner, err := s.cfg.Owner.Owner(ctx)
	if err != nil {
		slog.Warn("signins: hostd did not give the Account", "err", err)
	}
	if strings.TrimSpace(owner.Phone) == "" {
		missing = append(missing, "phone")
	}
	if strings.TrimSpace(owner.FirstName) == "" || strings.TrimSpace(owner.LastName) == "" {
		missing = append(missing, "names")
	}
	if len(missing) > 0 {
		return "", Owner{}, &NotReadyError{Missing: missing}
	}
	return mailbox, owner, nil
}

// startAccount starts over on the site: an interrupted attempt's profile forgotten by the worker
// (first: a worker that isn't there leaves no orphan secret), a password generated and kept in
// secretd (one it can't keep is no registration), the attempt's plus-address journaled, and the
// account written as interrupted until the run says otherwise. It returns the values the errand fills.
func (s *Service) startAccount(ctx context.Context, key, mailbox string, owner Owner) (map[string]string, Account, error) {
	if err := s.forgetProfile(ctx, key); err != nil {
		return nil, Account{}, err
	}
	password := newPassword()
	if err := s.cfg.Secrets.Put(ctx, passwordName(key), password); err != nil {
		return nil, Account{}, err
	}
	email, err := s.nextAddress(ctx, key, mailbox)
	if err != nil {
		return nil, Account{}, err
	}
	// Journaled first: a row never exists without the journal entry that keeps its address.
	if err := s.cfg.Store.Journal(ctx, key, "registering", email, owner.Phone); err != nil {
		return nil, Account{}, err
	}
	a := Account{Key: key, Email: email, State: "warn", Reason: "the registration was interrupted",
		Owner: map[string]string{"first_name": owner.FirstName, "last_name": owner.LastName}, CreatedAt: s.cfg.Now()}
	if err := s.cfg.Store.PutAccount(ctx, a); err != nil {
		return nil, Account{}, err
	}
	// The full name too: many forms have one name box, and a field the model can't name it asks the
	// owner for.
	return map[string]string{"email": email, "first_name": owner.FirstName, "last_name": owner.LastName,
		"full_name": owner.FirstName + " " + owner.LastName, "phone": owner.Phone, "secret:password": password}, a, nil
}

// registrationEnded records how a registration ended. Only the model's word that it was done counts: then
// the account is the box's, and a sign-in check follows once the site is free (its model sign-in
// leaves the account's first footprint). Cut short (Stop, the engine or the worker going), nothing is known of the
// site's side: the account stays interrupted with its profile, and Register starts over. Otherwise
// there is no account: its row goes and the worker forgets its profile (a bot wall's cookie in it
// would block the next try).
func (s *Service) registrationEnded(ctx context.Context, a Account, run int64, reply browser.Message, err error, ended string) (next func()) {
	ctx = context.WithoutCancel(ctx)
	a.Run = run
	switch {
	case err == nil && ended == "" && reply["success"] == true:
		a.State, a.Reason = "registered", ""
		if err := s.cfg.Store.Journal(ctx, a.Key, "registered", a.Email, ""); err != nil {
			slog.Error("signins: journaling a registration", "site", a.Key, "err", err)
		}
		next = func() {
			if _, err := s.CheckSignIn(s.cfg.Base, a.Key); err != nil {
				slog.Warn("signins: the sign-in after a registration did not start", "site", a.Key, "err", err)
			}
		}
	case ended == "stopped", ended == "" && (errors.Is(err, context.Canceled) || errors.Is(err, browser.ErrUnavailable)):
		a.State, a.Reason = "warn", "the registration was interrupted"
	default:
		if err := s.forgetProfile(ctx, a.Key); err != nil {
			slog.Warn("signins: forgetting the profile of a failed registration", "site", a.Key, "err", err)
		}
		// A password left behind is overwritten by the next Register on the site.
		if err := s.cfg.Secrets.Delete(ctx, passwordName(a.Key)); err != nil {
			slog.Error("signins: removing a failed registration's password", "site", a.Key, "err", err)
		}
		if err := s.cfg.Store.DeleteAccount(ctx, a.Key); err != nil {
			slog.Error("signins: removing a failed registration", "site", a.Key, "err", err)
		}
		return nil
	}
	if err := s.cfg.Store.PutAccount(ctx, a); err != nil {
		slog.Error("signins: storing a registration", "site", a.Key, "err", err)
	}
	return next
}

// smsCode answers the worker's sms_code: the code of an SMS to the box's SIM since the run started.
// The code is never logged.
func (s *Service) smsCode(ctx context.Context, e *agentRun, log *runLog) (string, bool) {
	if e.sms == nil {
		return "", false
	}
	// An smsd that never said it is listening can't be counted on for the code: miss it now rather
	// than wait out the code's whole clock on a daemon that is down.
	if !e.sms.Listening() {
		slog.Warn("signins: SMS archive unreadable", "site", e.site)
		log.box("The SMS archive could not be read")
		return "", false
	}
	log.box("Waiting for the SMS code")
	// The owner may act on the page meanwhile: the site may show why no code is coming.
	end := s.openWait(e.site, &ownerWait{until: s.cfg.Now().Add(s.codes), kind: waitCode}, phaseWaitingCode)
	found, ok := e.sms.Take(ctx, s.codes)
	end()
	switch {
	case ok:
		log.box("The SMS code arrived")
	case ctx.Err() == nil:
		log.box("No SMS code came")
	}
	return found, ok
}

// nextEmail answers the worker's email: the next email to the account's address stored since the
// run started that the worker hasn't taken yet, as JSON subject, text and html. Its links and codes are the worker's to keep from the model; nothing of it is logged.
func (s *Service) nextEmail(ctx context.Context, e *agentRun, log *runLog) (string, bool) {
	if e.email == "" || s.cfg.Inbox == nil {
		return "", false
	}
	// The next email after the ones the worker has taken: an ask that came back empty took none.
	s.mu.Lock()
	n := e.emails + 1
	s.mu.Unlock()
	log.box("Waiting for the site's email")
	end := s.openWait(e.site, &ownerWait{until: s.cfg.Now().Add(s.codes), kind: waitCode}, phaseWaitingCode)
	defer end()
	wait, cancel := context.WithTimeout(ctx, s.codes)
	defer cancel()
	seen := 0
	for ev := range s.cfg.Inbox.Listen(wait, &listenv1.ListenRequest{To: e.email, Since: timestamppb.New(e.since)}) {
		m := ev.GetMessage().GetEmail()
		if m == nil {
			continue
		}
		if seen++; seen < n {
			continue
		}
		raw, err := json.Marshal(map[string]string{"subject": m.GetSubject(), "text": m.GetText(), "html": m.GetHtml()})
		if err != nil {
			return "", false
		}
		s.mu.Lock()
		e.emails = n
		s.mu.Unlock()
		log.box("An email arrived")
		return string(raw), true
	}
	if ctx.Err() == nil {
		log.box("No email came")
	}
	return "", false
}
