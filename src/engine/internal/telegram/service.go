package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/gotd/td/telegram/auth"
)

// Status: State is no_credentials, unlinked, pairing, password_needed, connecting, connected or
// logged_out. The api_hash is never in it.
type Status struct {
	State           string `json:"state"`
	Credentials     bool   `json:"credentials"`
	APIID           int    `json:"api_id,omitempty"`
	Phone           string `json:"phone,omitempty"`
	Name            string `json:"name,omitempty"`
	ArchiveChannels bool   `json:"archive_channels"`
}

const messagesShown = 500

const (
	defaultPairingTimeout = 5 * time.Minute
	runRetry              = 10 * time.Second
	logoutTimeout         = 30 * time.Second
)

type Service struct {
	c     Client
	store *Store
	ss    *SessionStore
	us    *UpdateState

	pairingTimeout time.Duration
	retryDelay     time.Duration
	logoutTimeout  time.Duration

	mu              sync.Mutex
	state           string
	creds           *Creds
	self            Self
	archiveChannels bool
	qrURL           string
	qrExpires       time.Time
	unlinking       bool
	stopRun         context.CancelFunc
	runDone         chan struct{} // closed when the current run has ended
	stopPairing     context.CancelFunc
	wg              sync.WaitGroup

	backfilling sync.Mutex    // one first backfill at a time
	readTimeout time.Duration // one mark sent to Telegram
	nudge       chan struct{} // asks flushLoop to flush now
	flushing    sync.Mutex    // one flush at a time, so a mark is not sent twice

	life context.Context // ends with Stop; background work (backfill) runs under it
	end  context.CancelFunc
}

func NewService(c Client, s *Store, ss *SessionStore, us *UpdateState) *Service {
	life, end := context.WithCancel(context.Background())
	return &Service{c: c, store: s, ss: ss, us: us, state: "no_credentials",
		pairingTimeout: defaultPairingTimeout, retryDelay: runRetry, logoutTimeout: logoutTimeout, readTimeout: readTimeout, nudge: make(chan struct{}, 1), life: life, end: end}
}

// Start loads what telegram.db holds and, with credentials, keeps a connection running: a linked
// session reconnects without a QR code; a pairing interrupted by a restart is simply unlinked.
func (s *Service) Start(ctx context.Context) error {
	s.background(s.flushLoop)
	cfg := map[string]string{}
	for _, k := range []string{"api_id", "api_hash", "archive_channels", "link", "self"} {
		v, err := s.store.Get(ctx, k)
		if err != nil {
			return err
		}
		cfg[k] = v
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archiveChannels = cfg["archive_channels"] == "1"
	s.us.TrackBroadcasts(s.archiveChannels)
	if cfg["self"] != "" {
		if err := json.Unmarshal([]byte(cfg["self"]), &s.self); err != nil {
			return err
		}
	}
	if cfg["api_id"] == "" {
		return nil
	}
	id, err := strconv.Atoi(cfg["api_id"])
	if err != nil {
		return err
	}
	s.creds = &Creds{ID: id, Hash: cfg["api_hash"]}
	switch {
	case cfg["link"] == "logged_out":
		s.state = "logged_out"
	case s.self.ID != 0:
		s.state = "connecting"
	default:
		s.state = "unlinked"
	}
	s.startRun()
	return nil
}

// Stop ends the connection and any pairing.
func (s *Service) Stop() {
	s.mu.Lock()
	if s.stopRun != nil {
		s.stopRun()
	}
	if s.stopPairing != nil {
		s.stopPairing()
	}
	s.mu.Unlock()
	s.end()
	s.wg.Wait()
}

// startRun (re)starts the connection with the current credentials; s.mu held.
func (s *Service) startRun() {
	if s.stopRun != nil {
		s.stopRun()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.stopRun = cancel
	c := *s.creds
	prev, done := s.runDone, make(chan struct{})
	s.runDone = done
	s.wg.Add(1)
	go s.run(ctx, c, prev, done)
}

// run starts only once the previous run has ended: gotd's client is one connection at a time.
func (s *Service) run(ctx context.Context, c Creds, prev <-chan struct{}, done chan<- struct{}) {
	defer s.wg.Done()
	defer close(done)
	if prev != nil {
		<-prev
	}
	for {
		err := s.c.Run(ctx, c, s.handle)
		if ctx.Err() != nil {
			return
		}
		if auth.IsUnauthorized(err) {
			s.unauthorized(ctx)
			continue // a fresh, signed-out connection, ready to link again
		}
		slog.Warn("telegram: connection ended, will retry", "err", err, "in", s.retryDelay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.retryDelay):
		}
	}
}

// unauthorized: Telegram ended the session (from the phone's Devices, or expired).
func (s *Service) unauthorized(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unlinking || (s.state != "connecting" && s.state != "connected") {
		return
	}
	if err := s.store.Set(ctx, "link", "logged_out"); err != nil {
		slog.Error("telegram: persisting logged_out", "err", err)
	}
	if err := s.forget(ctx); err != nil {
		slog.Error("telegram: forgetting the session", "err", err)
	}
	s.state = "logged_out"
}

// forget drops the session and the linked account, so the next link backfills afresh; s.mu held.
func (s *Service) forget(ctx context.Context) error {
	s.self = Self{}
	for _, k := range []string{"self", "backfilled"} {
		if err := s.store.Set(ctx, k, ""); err != nil {
			return err
		}
	}
	return s.ss.Forget(ctx)
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{State: s.state, Credentials: s.creds != nil, ArchiveChannels: s.archiveChannels}
	if s.creds != nil {
		st.APIID = s.creds.ID
	}
	if s.self.ID != 0 {
		st.Phone, st.Name = s.self.Phone, s.self.Name
	}
	return st
}

// SetCredentials checks a pair against Telegram and saves it. Only while not linked: a session is
// bound to the credentials that created it.
func (s *Service) SetCredentials(ctx context.Context, c Creds) error {
	s.mu.Lock()
	st := s.state
	s.mu.Unlock()
	if st != "no_credentials" && st != "unlinked" && st != "logged_out" {
		return ErrState
	}
	if err := s.c.Check(ctx, c); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != st {
		return ErrState
	}
	if err := s.store.Set(ctx, "api_id", strconv.Itoa(c.ID)); err != nil {
		return err
	}
	if err := s.store.Set(ctx, "api_hash", c.Hash); err != nil {
		return err
	}
	s.creds = &c
	if s.state == "no_credentials" {
		s.state = "unlinked"
	}
	s.startRun()
	return nil
}

// Link starts a QR login in the background; it gives up after pairingTimeout.
func (s *Service) Link(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlinked" && s.state != "logged_out" {
		return ErrState
	}
	s.state = "pairing"
	ctx, cancel := context.WithTimeout(context.Background(), s.pairingTimeout)
	s.stopPairing = cancel
	s.wg.Add(1)
	go s.pair(ctx, cancel)
	return nil
}

func (s *Service) pair(ctx context.Context, cancel context.CancelFunc) {
	defer s.wg.Done()
	defer cancel()
	self, err := s.c.QR(ctx, func(url string, expires time.Time) {
		s.mu.Lock()
		s.qrURL, s.qrExpires = url, expires
		s.mu.Unlock()
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.qrURL = ""
	if s.state != "pairing" {
		return
	}
	switch {
	case err == nil:
		s.linked(self)
	case errors.Is(err, ErrPasswordNeeded):
		s.state = "password_needed"
	default:
		if ctx.Err() == nil {
			slog.Warn("telegram: QR login failed", "err", err)
		}
		s.state = "unlinked"
	}
}

// linked persists the session and the account; s.mu held.
func (s *Service) linked(self Self) {
	ctx := context.Background()
	if err := s.ss.Persist(ctx); err != nil {
		slog.Error("telegram: persisting the session", "err", err)
	}
	b, _ := json.Marshal(self)
	if err := s.store.Set(ctx, "self", string(b)); err != nil {
		slog.Error("telegram: persisting the account", "err", err)
	}
	if err := s.store.Set(ctx, "link", ""); err != nil {
		slog.Error("telegram: clearing logged_out", "err", err)
	}
	s.self = self
	s.state = "connecting"
	s.c.Proceed()
}

// QR is the current login URL to show as a QR code while pairing.
func (s *Service) QR() (url string, expiresIn time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "pairing" || s.qrURL == "" {
		return "", 0, false
	}
	return s.qrURL, time.Until(s.qrExpires), true
}

// Password finishes a QR login on an account with a cloud password. The password is used for this
// call only.
func (s *Service) Password(ctx context.Context, pw string) error {
	s.mu.Lock()
	st := s.state
	s.mu.Unlock()
	if st != "password_needed" {
		return ErrState
	}
	self, err := s.c.Password(ctx, pw)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == "password_needed" {
		s.linked(self)
	}
	return nil
}

// Unlink logs the box out of the account; with deleteChats it also deletes that account's archive.
// The credentials stay.
func (s *Service) Unlink(ctx context.Context, deleteChats bool) error {
	s.mu.Lock()
	if s.state != "connecting" && s.state != "connected" {
		s.mu.Unlock()
		return ErrState
	}
	account := acct(s.self.ID)
	s.unlinking = true
	s.mu.Unlock()

	lctx, cancel := context.WithTimeout(ctx, s.logoutTimeout) // no connection: say so, don't hang
	err := s.c.LogOut(lctx)
	cancel()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.unlinking = false
	if err != nil && !auth.IsUnauthorized(err) { // a 401: Telegram already ended the session
		return err
	}
	if err := s.forget(ctx); err != nil {
		return err
	}
	if deleteChats {
		if err := s.store.DeleteAccount(ctx, account); err != nil {
			return err
		}
	}
	s.state = "unlinked"
	s.startRun() // a fresh, signed-out connection
	return nil
}

// Remove is the owner removing Telegram, in any state. A linked box logs out; when that cannot reach
// Telegram it changes nothing (ErrLogout) unless the owner approved (force), and then the session is
// forgotten locally. Any pairing ends. Keep keeps the archive and the app credentials; deleteData
// wipes everything telegram.db holds, credentials included. Repeating it succeeds.
func (s *Service) Remove(ctx context.Context, deleteData, force bool) error {
	s.mu.Lock()
	online := s.state == "connecting" || s.state == "connected"
	s.unlinking = true // a 401 from the session being ended is ours, not Telegram's
	s.mu.Unlock()
	if online {
		lctx, cancel := context.WithTimeout(ctx, s.logoutTimeout)
		err := s.c.LogOut(lctx)
		cancel()
		if err != nil && !auth.IsUnauthorized(err) && !force {
			s.mu.Lock()
			s.unlinking = false
			s.mu.Unlock()
			return fmt.Errorf("%w: %v", ErrLogout, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unlinking = false
	if s.stopPairing != nil {
		s.stopPairing()
	}
	if err := s.forget(ctx); err != nil {
		return err
	}
	if deleteData {
		// End the connection first and wait for it: an update still being stored would otherwise
		// land after the wipe.
		if s.stopRun != nil {
			s.stopRun()
		}
		done := s.runDone
		s.mu.Unlock()
		if done != nil {
			<-done
		}
		s.mu.Lock()
		if err := s.store.Wipe(ctx); err != nil {
			return err
		}
		s.creds, s.archiveChannels, s.state = nil, false, "no_credentials"
		s.us.TrackBroadcasts(false)
		return nil
	}
	if err := s.store.Set(ctx, "link", ""); err != nil {
		return err
	}
	if s.creds == nil {
		s.state = "no_credentials"
		return nil
	}
	s.state = "unlinked"
	s.startRun() // a fresh, signed-out connection
	return nil
}

func (s *Service) account() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.self.ID == 0 {
		return ""
	}
	return acct(s.self.ID)
}

// handle runs on the connection's goroutine.
func (s *Service) handle(evt any) {
	ctx := s.life
	switch e := evt.(type) {
	case NewMessage:
		s.message(ctx, e.Msg, e.Ent, false)
	case EditMessage:
		s.message(ctx, e.Msg, e.Ent, true)
	case DeletedCommon:
		if account := s.account(); account != "" {
			if err := s.store.DeleteCommon(ctx, account, e.IDs); err != nil {
				slog.Error("telegram: deleting messages", "err", err)
			}
		}
	case DeletedChannel:
		if account := s.account(); account != "" {
			if err := s.store.DeleteChannel(ctx, account, e.ChannelID, e.IDs); err != nil {
				slog.Error("telegram: deleting channel messages", "channel", e.ChannelID, "err", err)
			}
		}
	case TooLong:
		s.tooLong(e.ChannelID)
	case ReadInbox:
		// Read on another device: Telegram knows, so nothing to tell.
		if account := s.account(); account != "" {
			if err := s.store.ReadUpTo(ctx, account, e.Peer, e.MaxID); err != nil {
				slog.Error("telegram: storing a read elsewhere", "peer", e.Peer, "err", err)
			}
		}
	case Connected:
		s.mu.Lock()
		if s.state == "connecting" {
			s.state = "connected"
		}
		s.mu.Unlock()
		s.background(func(ctx context.Context) {
			if err := s.c.Offline(ctx); err != nil { // never shown as online
				slog.Warn("telegram: offline status", "err", err)
			}
			s.firstBackfill(ctx)
			s.seedRead(ctx)
			s.flushReads(ctx)
		})
	}
}
