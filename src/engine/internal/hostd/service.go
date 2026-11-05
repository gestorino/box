package hostd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

var (
	ErrBusy      = errors.New("another change is pending")
	ErrNotFound  = errors.New("not found")
	ErrNotTested = errors.New("test it first")
	ErrActive    = errors.New("the box is using this network; connect to another one first")
)

// ChangeRollback is how long a change waits for the owner's confirmation before NetworkManager
// undoes it by itself.
const ChangeRollback = 60 * time.Second

// Service holds the one slot for a change in flight: a pending checkpoint, or a running Wi-Fi test.
type Service struct {
	nm    NetworkManager
	mm    ModemManager
	units Units
	st    *Settings
	now   func() time.Time

	mu    sync.Mutex
	slot  *slot
	tests wifiTests
}

type slot struct {
	id, kind, path string
	expires        time.Time // zero while the slot is reserved but not yet a checkpoint
}

func NewService(nm NetworkManager, mm ModemManager, units Units, st *Settings, now func() time.Time) *Service {
	return &Service{nm: nm, mm: mm, units: units, st: st, now: now}
}

func (s *Service) Wifi(ctx context.Context) (WifiStatus, error) { return s.nm.Wifi(ctx) }

// Uplink is wifi, mobile, wired or none: what the box's traffic leaves by right now.
func (s *Service) Uplink(ctx context.Context) (string, error) { return s.nm.Uplink(ctx) }

// Scan returns one entry per network name, strongest first; hidden networks (no name) are dropped.
func (s *Service) Scan(ctx context.Context) ([]VisibleNetwork, error) {
	raw, err := s.nm.Scan(ctx)
	if err != nil {
		return nil, err
	}
	best := map[string]int{}
	var out []VisibleNetwork
	for _, n := range raw {
		if n.SSID == "" {
			continue
		}
		if i, seen := best[n.SSID]; seen {
			if n.Signal > out[i].Signal {
				out[i] = n
			}
			continue
		}
		best[n.SSID] = len(out)
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Signal > out[j].Signal })
	return out, nil
}

func (s *Service) Connect(ctx context.Context, id string) (Checkpoint, error) {
	if _, err := s.saved(ctx, id); err != nil {
		return Checkpoint{}, err
	}
	return s.change(ctx, "wifi-connect", DeviceWifi, func() error {
		_, err := s.nm.ActivateWifi(ctx, id, 0)
		return err
	})
}

// Forget refuses the network in use: deleting it drops the box off the owner's LAN, and on this box
// the profile is netplan-generated, which a checkpoint does not fully restore.
func (s *Service) Forget(ctx context.Context, id string) (Checkpoint, error) {
	n, err := s.saved(ctx, id)
	if err != nil {
		return Checkpoint{}, err
	}
	if n.Active {
		return Checkpoint{}, ErrActive
	}
	return s.change(ctx, "wifi-forget", DeviceWifi, func() error { return s.nm.DeleteConnection(ctx, id) })
}

func (s *Service) Cellular(ctx context.Context) (CellularStatus, error) {
	modem, sim, err := s.mm.Modem(ctx)
	if err != nil {
		return CellularStatus{}, err
	}
	data, err := s.nm.CellularData(ctx)
	if err != nil {
		return CellularStatus{}, err
	}
	return CellularStatus{Modem: modem, SIM: sim, Data: data}, nil
}

func (s *Service) UpdateCellular(ctx context.Context, u CellularUpdate) (Checkpoint, error) {
	return s.change(ctx, "cellular", DeviceModem, func() error { return s.nm.UpdateCellular(ctx, u) })
}

// Pending is the change waiting for confirmation, or nil — including once its rollback time has
// passed, because by then NetworkManager has undone it.
func (s *Service) Pending() *Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pendingLocked()
	if p == nil {
		return nil
	}
	left := p.expires.Sub(s.now())
	return &Checkpoint{ID: p.id, Kind: p.kind, RollbackInS: int((left + time.Second - 1) / time.Second)}
}

func (s *Service) Confirm(ctx context.Context, id string) error {
	return s.finish(ctx, id, s.nm.CheckpointDestroy)
}

func (s *Service) Rollback(ctx context.Context, id string) error {
	return s.finish(ctx, id, s.nm.CheckpointRollback)
}

func (s *Service) finish(ctx context.Context, id string, op func(context.Context, string) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pendingLocked()
	if p == nil || p.id != id {
		return ErrNotFound
	}
	if err := op(ctx, p.path); err != nil {
		return err
	}
	s.slot = nil
	return nil
}

// pendingLocked is the slot when it holds a live checkpoint; an expired one is cleared.
func (s *Service) pendingLocked() *slot {
	if s.slot == nil || s.slot.expires.IsZero() {
		return nil
	}
	if !s.now().Before(s.slot.expires) {
		s.slot = nil
		return nil
	}
	return s.slot
}

// reserve takes the one slot, or fails with ErrBusy.
func (s *Service) reserve(kind string) (*slot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slot != nil && (s.slot.expires.IsZero() || s.pendingLocked() != nil) {
		return nil, ErrBusy
	}
	s.slot = &slot{id: newID(), kind: kind}
	return s.slot, nil
}

func (s *Service) release(sl *slot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slot == sl {
		s.slot = nil
	}
}

// change runs act under a checkpoint. The checkpoint exists before anything changes; if act fails,
// the change is rolled back at once rather than left to the timeout.
func (s *Service) change(ctx context.Context, kind string, d Device, act func() error) (Checkpoint, error) {
	sl, err := s.reserve(kind)
	if err != nil {
		return Checkpoint{}, err
	}
	path, err := s.nm.CheckpointCreate(ctx, d, ChangeRollback)
	if err != nil {
		s.release(sl)
		return Checkpoint{}, err
	}
	if err := act(); err != nil {
		s.nm.CheckpointRollback(ctx, path)
		s.release(sl)
		return Checkpoint{}, err
	}
	s.mu.Lock()
	sl.path, sl.expires = path, s.now().Add(ChangeRollback)
	s.mu.Unlock()
	return Checkpoint{ID: sl.id, Kind: kind, RollbackInS: int(ChangeRollback / time.Second)}, nil
}

func (s *Service) saved(ctx context.Context, id string) (SavedNetwork, error) {
	w, err := s.nm.Wifi(ctx)
	if err != nil {
		return SavedNetwork{}, err
	}
	for _, n := range w.Saved {
		if n.ID == id {
			return n, nil
		}
	}
	return SavedNetwork{}, ErrNotFound
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
