package hostd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
	"time"
)

const (
	// TestWait is how long a test waits for the network to authenticate and hand out an address.
	TestWait = 30 * time.Second
	// TestSafety is the test checkpoint's own NetworkManager timeout: if hostd dies mid-test,
	// NetworkManager still brings the box back.
	TestSafety = 90 * time.Second
	// TestedValidFor is how long a green test lets the same network and password be saved.
	TestedValidFor = 10 * time.Minute
)

// TestResult: State is running, passed or failed; Reason (when failed) as in Activation.
type TestResult struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	IP4    string `json:"ip4,omitempty"`
}

// wifiTests is guarded by Service.mu. passed holds salted hashes of tested (ssid, password) pairs
// — never the password.
type wifiTests struct {
	results map[string]TestResult
	passed  map[[32]byte]time.Time
	salt    [32]byte
}

// StartTest tries a network in the background and always comes back to the previous one. It takes
// the same single slot as a change, so nothing else can change while the box is away.
func (s *Service) StartTest(_ context.Context, ssid, password string) (string, error) {
	sl, err := s.reserve("wifi-test")
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.testsLocked().results[sl.id] = TestResult{State: "running"}
	s.mu.Unlock()
	go s.runTest(sl, ssid, password)
	return sl.id, nil
}

func (s *Service) runTest(sl *slot, ssid, password string) {
	ctx, cancel := context.WithTimeout(context.Background(), TestSafety)
	defer cancel()
	result := TestResult{State: "failed", Reason: "unavailable"}
	defer func() {
		s.mu.Lock()
		s.testsLocked().results[sl.id] = result
		if result.State == "passed" {
			s.testsLocked().passed[s.pairHash(ssid, password)] = s.now().Add(TestedValidFor)
		}
		s.mu.Unlock()
		s.release(sl)
	}()

	path, err := s.nm.CheckpointCreate(ctx, DeviceWifi, TestSafety)
	if err != nil {
		return
	}
	defer s.nm.CheckpointRollback(ctx, path)
	id, err := s.nm.AddWifi(ctx, NewWifi{SSID: ssid, Password: password, InMemory: true})
	if err != nil {
		return
	}
	a, err := s.nm.ActivateWifi(ctx, id, TestWait)
	switch {
	case err != nil:
	case a.Connected:
		result = TestResult{State: "passed", IP4: a.IP4}
	case a.Reason != "":
		result.Reason = a.Reason
	}
}

func (s *Service) TestResult(id string) (TestResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.testsLocked().results[id]
	return r, ok
}

// Save stores a network for good — only the exact network and password a test passed with, within
// TestedValidFor. It changes nothing now: the saved network ranks below the active one. It takes
// the slot: a checkpoint deletes connections created after it, so a save during one would vanish.
func (s *Service) Save(ctx context.Context, ssid, password string) (SavedNetwork, error) {
	sl, err := s.reserve("wifi-save")
	if err != nil {
		return SavedNetwork{}, err
	}
	defer s.release(sl)
	s.mu.Lock()
	t := s.testsLocked()
	h := s.pairHash(ssid, password)
	expires, ok := t.passed[h]
	if ok {
		delete(t.passed, h)
	}
	s.mu.Unlock()
	if !ok || !s.now().Before(expires) {
		return SavedNetwork{}, ErrNotTested
	}
	id, err := s.nm.AddWifi(ctx, NewWifi{SSID: ssid, Password: password, Autoconnect: true, Priority: -1})
	if err != nil {
		return SavedNetwork{}, redact(err, password)
	}
	return SavedNetwork{ID: id, SSID: ssid}, nil
}

// testsLocked lazily initialises the test state; the caller holds s.mu.
func (s *Service) testsLocked() *wifiTests {
	if s.tests.results == nil {
		s.tests.results = map[string]TestResult{}
		s.tests.passed = map[[32]byte]time.Time{}
		rand.Read(s.tests.salt[:])
	}
	return &s.tests
}

// pairHash: the caller holds s.mu (the salt is set under it).
func (s *Service) pairHash(ssid, password string) [32]byte {
	h := sha256.New()
	h.Write(s.tests.salt[:])
	h.Write([]byte(ssid))
	h.Write([]byte{0})
	h.Write([]byte(password))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// redact strips a secret out of an error NetworkManager returned for a request that carried it, so
// it can be logged. The error's type is not kept: nothing branches on NetworkManager's errors.
func redact(err error, secret string) error {
	if secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "[redacted]"))
}
