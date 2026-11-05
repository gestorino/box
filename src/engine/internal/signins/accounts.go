package signins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"concierge/engine/internal/browser"
)

// ErrUnknownSite is a site the catalog holds no card of.
var ErrUnknownSite = errors.New("no such site in the catalog")

// ErrNoAccount refuses an operation on an account the box does not hold.
var ErrNoAccount = errors.New("the box has no account on this site")

// ErrCardInUse refuses removing a card while the box has an account on it or a run works on it.
var ErrCardInUse = errors.New("the box has an account on this site, or is working on it")

// ErrWaitingForWiFi refuses a run while the box is on mobile data: a browser run is tens of megabytes
// on a metered SIM.
var ErrWaitingForWiFi = errors.New("waiting for Wi-Fi: the box is on mobile data")

// NotReadyError lists what the box lacks to register: "email" (no connected mailbox), "phone" (the
// box does not know its SIM's number), "names" (the Account has no names).
type NotReadyError struct{ Missing []string }

func (e *NotReadyError) Error() string {
	return "not ready: " + strings.Join(e.Missing, ", ")
}

// siteKey is a host's registrable domain (eTLD+1), lower case: booksy.com for pt.booksy.com and
// www.booksy.com alike. A site's card, account, profile, runs and recordings share it. An IP address
// or a single-label host (localhost) is its own key.
func siteKey(host string) (string, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return "", errors.New("no host")
	}
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return host, nil
	}
	key, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", fmt.Errorf("%s has no registrable domain: %w", host, err)
	}
	return key, nil
}

// validKey is what a key may look like where it names a directory or a URL path: host labels, dot
// separated. Nothing that could leave the directory it names.
var validKey = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

func isKey(key string) bool { return len(key) <= 253 && validKey.MatchString(key) }

// AccountView is the box's account on a site as the console shows it. It never carries the password.
type AccountView struct {
	Email string `json:"email"`
	// State is registered or warn (Reason says why).
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	Run       int64     `json:"run"` // its registration run
	CreatedAt time.Time `json:"created_at"`
	// Footprint is the page a check reads without the model; "" when the next check is the model's.
	// FootprintClicks is how many clicks follow its link (their names stay on the box).
	Footprint       string `json:"footprint"`
	FootprintClicks int    `json:"footprint_clicks"`
}

// SiteView is one card of the console's Accounts page, with the box's account on it if any.
type SiteView struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Link        string `json:"link"`
	HasIcon     bool   `json:"has_icon"`
	// Actions are what the owner can start an errand with here.
	Actions []Action     `json:"actions"`
	Account *AccountView `json:"account,omitempty"`
	// Phase is what a run in flight does on the site (registering, errand, checking, waiting,
	// waiting_code), and Run that run; "" and 0 when none.
	Phase string `json:"phase,omitempty"`
	Run   int64  `json:"run,omitempty"`
	// Latest is the newest of the site's registrations, sign-in checks and sign-ins: how the box last
	// stood there. nil when it has none.
	Latest *AccountRun `json:"latest,omitempty"`
}

func (s *Service) siteView(ctx context.Context, c Card) (SiteView, error) {
	v := SiteView{Key: c.Key, Name: c.Name, Description: c.Description, Link: c.Link, HasIcon: c.Icon != "",
		Actions: c.Actions}
	a, has, err := s.cfg.Store.Account(ctx, c.Key)
	if err != nil {
		return SiteView{}, err
	}
	if has {
		v.Account = &AccountView{Email: a.Email, State: a.State, Reason: a.Reason, Run: a.Run, CreatedAt: a.CreatedAt,
			Footprint: a.Footprint, FootprintClicks: len(a.FootprintClicks)}
	}
	latest, err := s.cfg.Store.SiteRuns(ctx, c.Key, 1)
	if err != nil {
		return SiteView{}, err
	}
	if len(latest) == 1 {
		v.Latest = &latest[0]
	}
	if p, _, run := s.running(c.Key); p != phaseRemoving {
		v.Phase, v.Run = p, run
	}
	return v, nil
}

// Sites is every card, with the box's account on it.
func (s *Service) Sites(ctx context.Context) ([]SiteView, error) {
	cards, err := s.cfg.Store.Cards(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SiteView, 0, len(cards))
	for _, c := range cards {
		v, err := s.siteView(ctx, c)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Icon is the site's icon as PNG bytes; nil when it has none.
func (s *Service) Icon(ctx context.Context, key string) ([]byte, error) {
	c, found, err := s.cfg.Store.Card(ctx, key)
	if err != nil || !found {
		return nil, err
	}
	return iconPNG(c.Icon)
}

// RemoveAccount forgets the box's account on a site: the worker forgets its browser profile first (a
// later Register must not start inside the old account's session; no worker connected changes
// nothing), then its password in secretd and its row. The journal keeps a `removed` entry; the box
// never deletes the account on the site itself. Its runs stay in the Errands list, videos and all.
func (s *Service) RemoveAccount(ctx context.Context, key string) error {
	if !isKey(key) {
		return ErrNoAccount
	}
	if !s.claim(key, phaseRemoving) {
		return ErrBusy
	}
	defer s.release(key)
	a, has, err := s.cfg.Store.Account(ctx, key)
	if err != nil {
		return err
	}
	if !has {
		return ErrNoAccount
	}
	if err := s.forgetProfile(ctx, key); err != nil {
		return err
	}
	// The password before the row: a secretd that doesn't answer leaves the account to remove again.
	if err := s.cfg.Secrets.Delete(ctx, passwordName(key)); err != nil {
		return err
	}
	if err := s.cfg.Store.DeleteAccount(ctx, key); err != nil {
		return err
	}
	if err := s.cfg.Store.Journal(ctx, key, "removed", a.Email, s.phone(ctx)); err != nil {
		return err
	}
	slog.Info("signins: account removed", "site", key)
	return nil
}

// RemoveCard removes the owner's own card of a site the box has no account on and no run on.
func (s *Service) RemoveCard(ctx context.Context, key string) error {
	if _, found, err := s.cfg.Store.Card(ctx, key); err != nil {
		return err
	} else if !found {
		return ErrUnknownSite
	}
	if !s.claim(key, phaseRemoving) {
		return ErrCardInUse
	}
	defer s.release(key)
	if _, has, err := s.cfg.Store.Account(ctx, key); err != nil {
		return err
	} else if has {
		return ErrCardInUse
	}
	return s.cfg.Store.DeleteCard(ctx, key)
}

// forgetProfile asks the worker to delete the site's Chrome profile (its cookies and storage, whatever
// a bot wall left there), answered once it is gone; the worker waits out a run holding the site. A
// key that isn't one is never sent. No worker connected is browser.ErrUnavailable.
func (s *Service) forgetProfile(ctx context.Context, key string) error {
	if !isKey(key) {
		return fmt.Errorf("%q is not a site key", key)
	}
	if _, err := s.cfg.Worker.Request(ctx, "forget_profile", browser.Message{"site": key}); err != nil {
		return fmt.Errorf("forgetting the profile of %s: %w", key, err)
	}
	return nil
}

// Settings is what the Accounts page shows of the box's identity: the mailbox the plus-addresses
// belong to ("" when the Email connector is not connected). Screens says this computer gives its runs
// screens (a screens dir), so a run's page frames one.
type Settings struct {
	Email   string `json:"email"`
	Screens bool   `json:"screens"`
}

func (s *Service) Settings(ctx context.Context) Settings {
	email, err := s.cfg.Mail.Address(ctx)
	if err != nil {
		slog.Warn("signins: the Email connector did not answer", "err", err)
		email = ""
	}
	return Settings{Email: email, Screens: s.cfg.ScreensDir != ""}
}

// phone is the Account's phone number; "" when none is set or hostd doesn't answer.
func (s *Service) phone(ctx context.Context) string {
	owner, err := s.cfg.Owner.Owner(ctx)
	if err != nil {
		slog.Warn("signins: hostd did not give the Account's phone", "err", err)
		return ""
	}
	return owner.Phone
}

// offMobile holds a run off mobile data. hostd not answering counts as mobile, as emaild reads
// it: the safe guess on a metered link.
func (s *Service) offMobile(ctx context.Context) error {
	up, err := s.cfg.Uplink.Uplink(ctx)
	if err != nil {
		slog.Warn("signins: hostd did not say the uplink; waiting as on mobile data", "err", err)
		return ErrWaitingForWiFi
	}
	if up == "mobile" {
		return ErrWaitingForWiFi
	}
	return nil
}

func isAddress(addr string) bool {
	at := strings.LastIndex(addr, "@")
	return at > 0 && at < len(addr)-1
}

// nextAddress is the plus-address the next Register on the site uses: the mailbox tagged with the
// key's first label, numbered from the second attempt on (+booksy, +booksy2, …). A site routes an
// address it knows to sign-in, and any attempt may have left an account behind, so no attempt's
// address is used twice. "" when the mailbox is not an address.
func (s *Service) nextAddress(ctx context.Context, key, mailbox string) (string, error) {
	if !isAddress(mailbox) {
		return "", nil
	}
	n, err := s.cfg.Store.Registrations(ctx, key)
	if err != nil {
		return "", err
	}
	tag, _, _ := strings.Cut(key, ".")
	if n >= 1 {
		tag = fmt.Sprintf("%s%d", tag, n+1)
	}
	return plusAddress(mailbox, tag), nil
}

// plusAddress is the mailbox tagged: what the box gives a site so the site's mail reaches it.
func plusAddress(mailbox, tag string) string {
	at := strings.LastIndex(mailbox, "@")
	return mailbox[:at] + "+" + tag + mailbox[at:]
}

// checkAction is a model sign-in's action: the errand's one specific part (the task adds "for the
// owner"). It finishes on a page that proves the account, which becomes the footprint.
const checkAction = "sign in, and finish on the account's own page that shows its details"

// CheckSignIn is the owner's Check sign-in on a registered account: one run of kind check. With a
// footprint it first reads that page without the model (footprint.go): signed in ends it there, free.
// Signed out, broken, or no footprint: the model signs in from the card's link in the account's
// profile with its email and password, and the page it proves becomes the footprint. It ends "signed
// in" or "not signed in"; the account stays as it is. Only a check that may need the model needs the
// OpenRouter key up front: a free check that turns to the model without one ends on the worker's
// "no OpenRouter key".
func (s *Service) CheckSignIn(ctx context.Context, key string) (int64, error) {
	if s.cfg.Base.Err() != nil {
		return 0, ErrShuttingDown
	}
	c, found, err := s.cfg.Store.Card(ctx, key)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrUnknownSite
	}
	if err := s.offMobile(ctx); err != nil {
		return 0, err
	}
	if !s.claim(key, phaseChecking) {
		return 0, ErrBusy
	}
	// Under the claim: no Remove or Register changes the account meanwhile.
	a, _, err := s.cfg.Store.Account(ctx, key)
	var session string
	var domains []string
	var values map[string]string
	if err == nil {
		_, session, domains, values, err = s.errandSite(ctx, key)
	}
	if err == nil && session == "" {
		err = ErrNoAccount
	}
	if err == nil && a.Footprint == "" {
		err = s.keyed(ctx)
	}
	if err != nil {
		s.release(key)
		return 0, err
	}
	st := agentStart{key: key, kind: "check", url: c.Link, action: checkAction, session: session,
		domains: domains, values: values, email: values["email"], done: "signed in", notDone: "not signed in",
		prove:  browser.Message{"proofs": s.proofs(ctx, a), "old": oldFootprint(a)},
		proved: func(ctx context.Context, log *runLog, footprint any) { s.keepFootprint(ctx, log, key, footprint) }}
	if a.Footprint != "" {
		st.free = func(ctx context.Context, e *agentRun, log *runLog) bool { return s.readFootprint(ctx, e, log, a) }
	}
	return s.launch(ctx, st)
}

// oldFootprint is the account's footprint as the worker tries it first; nil when none.
func oldFootprint(a Account) map[string]any {
	if a.Footprint == "" {
		return nil
	}
	return map[string]any{"url": a.Footprint, "clicks": a.FootprintClicks}
}

// AccountRun is one run on a site as its page lists it: a registration, a sign-in check, or an errand
// that signed in with the box's password (it filled `secret:password`): a sign-in.
type AccountRun struct {
	AgentRunView
	SignedIn bool `json:"signed_in"`
}

// Site is one card's page: the card with the box's account on it, and the site's kept registrations,
// sign-in checks and sign-ins, newest first. found is false for a site with no card.
func (s *Service) Site(ctx context.Context, key string) (SiteView, []AccountRun, bool, error) {
	c, found, err := s.cfg.Store.Card(ctx, key)
	if err != nil || !found {
		return SiteView{}, nil, false, err
	}
	v, err := s.siteView(ctx, c)
	if err != nil {
		return SiteView{}, nil, false, err
	}
	runs, err := s.cfg.Store.SiteRuns(ctx, key, -1)
	if err != nil {
		return SiteView{}, nil, false, err
	}
	for i := range runs {
		runs[i].Waiting, _, _ = s.agentWaits(runs[i].Run)
	}
	return v, runs, true, nil
}

// signedInLine is the run-log line of an errand filling the box's password (runLog.progress's
// actionText of a fill): the errand signed in with it.
const signedInLine = "fill secret:password"

// SiteRuns is the site's kept registrations, sign-in checks and sign-ins (the errands that signed
// in), newest first, at most limit of them (-1: all). An errand that didn't sign in is the Errands
// page's, not the account's.
func (s *Store) SiteRuns(ctx context.Context, key string, limit int) ([]AccountRun, error) {
	const signedIn = `r.kind = 'errand' AND EXISTS (
		SELECT 1 FROM run_lines l WHERE l.run = r.id AND l.kind = 'action' AND l.text = ?)`
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentColumns+`, `+signedIn+agentTables+`
		WHERE r.site = ? AND (r.kind IN ('register', 'check') OR `+signedIn+`) ORDER BY r.id DESC LIMIT ?`,
		signedInLine, key, signedInLine, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountRun{}
	for rows.Next() {
		var r AccountRun
		r.AgentRunView, _, _, err = scanAgentRun(func(dest ...any) error { return rows.Scan(append(dest, &r.SignedIn)...) })
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
