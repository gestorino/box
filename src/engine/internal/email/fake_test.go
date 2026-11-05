package email

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"
)

// fakeServer is a mailbox and the server holding it; dial opens sessions on it.
type fakeServer struct {
	mu          sync.Mutex
	password    string
	uidValidity uint32
	msgs        []Fetched // ascending UID
	dials       int
	fetches     int
	failFetchAt int             // the Nth Fetch (1-based) fails with a dropped connection; 0 never
	failUIDs    map[uint32]bool // a Fetch naming any of these fails (Gmail's daily cap, say)
	onFetch     func()          // runs after each successful Fetch, outside the lock
	dialBlock   chan struct{}   // when set, dial waits for it to close…
	dialing     chan struct{}   // …after signalling here
	sessions    []*fakeSession
}

func newFakeServer() *fakeServer { return &fakeServer{password: "abcdefghijklmnop", uidValidity: 1} }

func (f *fakeServer) add(date time.Time, raw []byte) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	uid := uint32(1)
	if n := len(f.msgs); n > 0 {
		uid = f.msgs[n-1].UID + 1
	}
	f.msgs = append(f.msgs, Fetched{UID: uid, Date: date, Raw: raw})
	return uid
}

func (f *fakeServer) dial(_ context.Context, address, password string) (Session, error) {
	if f.dialBlock != nil {
		f.dialing <- struct{}{}
		<-f.dialBlock
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials++
	if address != me || password != f.password {
		return nil, ErrAuth
	}
	s := &fakeSession{f: f}
	f.sessions = append(f.sessions, s)
	return s, nil
}

type fakeSession struct {
	f      *fakeServer
	closed bool
}

func (s *fakeSession) UIDValidity() uint32 {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	return s.f.uidValidity
}
func (s *fakeSession) UIDNext() uint32 {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if n := len(s.f.msgs); n > 0 {
		return s.f.msgs[n-1].UID + 1
	}
	return 1
}

func (s *fakeSession) list(keep func(Fetched) bool) ([]uint32, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if s.closed {
		return nil, errors.New("closed")
	}
	var out []uint32
	for _, m := range s.f.msgs {
		if keep(m) {
			out = append(out, m.UID)
		}
	}
	return out, nil
}

func (s *fakeSession) UIDsSince(_ context.Context, day time.Time) ([]uint32, error) {
	return s.list(func(m Fetched) bool { return !m.Date.Before(day) })
}

func (s *fakeSession) UIDsAfter(_ context.Context, uid uint32) ([]uint32, error) {
	return s.list(func(m Fetched) bool { return m.UID > uid })
}

func (s *fakeSession) Fetch(_ context.Context, uids []uint32) ([]Fetched, error) {
	out, err := s.fetch(uids)
	if err == nil && s.f.onFetch != nil {
		s.f.onFetch()
	}
	return out, err
}

func (s *fakeSession) fetch(uids []uint32) ([]Fetched, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.fetches++
	if s.closed || s.f.fetches == s.f.failFetchAt {
		return nil, errors.New("connection reset")
	}
	for _, u := range uids {
		if s.f.failUIDs[u] {
			return nil, errors.New("[OVERQUOTA] daily download limit")
		}
	}
	want := map[uint32]bool{}
	for _, u := range uids {
		want[u] = true
	}
	var out []Fetched
	for _, m := range s.f.msgs {
		if want[m.UID] {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

// reconcileCall is one Reconcile as the fake saw it.
type reconcileCall struct {
	address, password string
	uidValidity       uint32
	mark              []uint32
}

// fakeGmail is the Reconcile port over a mailbox: it sets \Seen on what it is told to mark, then
// answers with the UIDs still unread.
type fakeGmail struct {
	mu       sync.Mutex
	unread   []uint32
	validity uint32 // the mailbox's UIDVALIDITY
	err      error
	during   func() // runs in the middle of a call, after the marks arrived
	calls    []reconcileCall
	called   chan struct{} // one send per call, when set
}

func newFakeGmail() *fakeGmail { return &fakeGmail{validity: 1} }

func (f *fakeGmail) reconcile(_ context.Context, address, password string, uidValidity uint32, mark []uint32) ([]uint32, error) {
	f.mu.Lock()
	f.calls = append(f.calls, reconcileCall{address, password, uidValidity, mark})
	err, during, called := f.err, f.during, f.called
	if err == nil && uidValidity != f.validity {
		err = ErrRenumbered
	}
	if err == nil {
		f.unread = slices.DeleteFunc(f.unread, func(u uint32) bool { return slices.Contains(mark, u) })
	}
	out := slices.Clone(f.unread)
	f.mu.Unlock()
	if called != nil {
		called <- struct{}{}
	}
	if err != nil {
		return nil, err
	}
	if during != nil {
		during()
	}
	return out, nil
}

func (f *fakeGmail) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (s *fakeSession) Wait(ctx context.Context, max time.Duration) error { return nil }
func (s *fakeSession) Close() error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.closed = true
	return nil
}
