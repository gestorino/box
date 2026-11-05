package hostd

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// fakeNM records every call in order and answers from its fields; err[method] makes that method fail.
type fakeNM struct {
	mu         sync.Mutex
	calls      []string
	err        map[string]error
	wifi       WifiStatus
	scan       []VisibleNetwork
	data       DataInfo
	activation Activation
	block      chan struct{} // when set, ActivateWifi waits for it to close
	nextCP     int
	uplink     string
}

func newFakeNM() *fakeNM {
	return &fakeNM{err: map[string]error{}, wifi: WifiStatus{
		Radio: true, State: "connected",
		Saved: []SavedNetwork{{ID: "11111111-1111-1111-1111-111111111111", SSID: "internet", Active: true}},
	}}
}

func (f *fakeNM) record(format string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := fmt.Sprintf(format, args...)
	f.calls = append(f.calls, call)
	for name, err := range f.err {
		if len(call) >= len(name) && call[:len(name)] == name {
			return err
		}
	}
	return nil
}

func (f *fakeNM) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeNM) Wifi(context.Context) (WifiStatus, error) { return f.wifi, f.record("Wifi") }
func (f *fakeNM) Scan(context.Context) ([]VisibleNetwork, error) {
	return f.scan, f.record("Scan")
}
func (f *fakeNM) AddWifi(_ context.Context, w NewWifi) (string, error) {
	err := f.record("AddWifi(%s,mem=%t,auto=%t,prio=%d)", w.SSID, w.InMemory, w.Autoconnect, w.Priority)
	return "22222222-2222-2222-2222-222222222222", err
}
func (f *fakeNM) ActivateWifi(_ context.Context, id string, wait time.Duration) (Activation, error) {
	err := f.record("ActivateWifi(%s,%s)", id, wait)
	if f.block != nil {
		<-f.block
	}
	return f.activation, err
}
func (f *fakeNM) DeleteConnection(_ context.Context, id string) error {
	return f.record("DeleteConnection(%s)", id)
}
func (f *fakeNM) Uplink(context.Context) (string, error) { return f.uplink, f.record("Uplink") }
func (f *fakeNM) CellularData(context.Context) (DataInfo, error) {
	return f.data, f.record("CellularData")
}
func (f *fakeNM) UpdateCellular(_ context.Context, u CellularUpdate) error {
	apn, data := "-", "-"
	if u.APN != nil {
		apn = *u.APN
	}
	if u.DataEnabled != nil {
		data = fmt.Sprint(*u.DataEnabled)
	}
	return f.record("UpdateCellular(apn=%s,data=%s)", apn, data)
}
func (f *fakeNM) CheckpointCreate(_ context.Context, d Device, rollback time.Duration) (string, error) {
	f.mu.Lock()
	f.nextCP++
	path := fmt.Sprintf("/cp/%d", f.nextCP)
	f.mu.Unlock()
	return path, f.record("CheckpointCreate(%s,%s)", d, rollback)
}
func (f *fakeNM) CheckpointDestroy(_ context.Context, path string) error {
	return f.record("CheckpointDestroy(%s)", path)
}
func (f *fakeNM) CheckpointRollback(_ context.Context, path string) error {
	return f.record("CheckpointRollback(%s)", path)
}

type fakeMM struct {
	modem ModemInfo
	sim   SIMInfo
	err   error
}

func (f *fakeMM) Modem(context.Context) (ModemInfo, SIMInfo, error) { return f.modem, f.sim, f.err }

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)} }
func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
