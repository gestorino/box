package hostd

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

const savedID = "11111111-1111-1111-1111-111111111111"

const otherID = "33333333-3333-3333-3333-333333333333"

func newService() (*Service, *fakeNM, *fakeMM, *fakeClock) {
	nm, mm, clock := newFakeNM(), &fakeMM{}, newFakeClock()
	nm.wifi.Saved = append(nm.wifi.Saved, SavedNetwork{ID: otherID, SSID: "cafe"})
	return NewService(nm, mm, nil, nil, clock.now), nm, mm, clock
}

var ctx = context.Background()

func TestConnectRunsUnderCheckpoint(t *testing.T) {
	s, nm, _, _ := newService()
	cp, err := s.Connect(ctx, savedID)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Kind != "wifi-connect" || cp.RollbackInS != 60 || cp.ID == "" {
		t.Fatalf("checkpoint %+v", cp)
	}
	want := []string{"Wifi", "CheckpointCreate(wifi,1m0s)", "ActivateWifi(" + savedID + ",0s)"}
	if got := nm.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

func TestConnectUnknownIDIsNotFound(t *testing.T) {
	s, nm, _, _ := newService()
	if _, err := s.Connect(ctx, "99999999-9999-9999-9999-999999999999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	for _, c := range nm.Calls() {
		if c != "Wifi" {
			t.Fatalf("no checkpoint may be created for an unknown id: %v", nm.Calls())
		}
	}
}

func TestSecondChangeIsBusy(t *testing.T) {
	s, _, _, _ := newService()
	if _, err := s.Connect(ctx, savedID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Forget(ctx, otherID); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
}

func TestConfirmDestroysCheckpoint(t *testing.T) {
	s, nm, _, _ := newService()
	cp, _ := s.Connect(ctx, savedID)
	if err := s.Confirm(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong id: want ErrNotFound, got %v", err)
	}
	if err := s.Confirm(ctx, cp.ID); err != nil {
		t.Fatal(err)
	}
	if calls := nm.Calls(); calls[len(calls)-1] != "CheckpointDestroy(/cp/1)" {
		t.Fatalf("calls %v", calls)
	}
	if s.Pending() != nil {
		t.Fatal("still pending after confirm")
	}
}

func TestRollbackRollsBack(t *testing.T) {
	s, nm, _, _ := newService()
	cp, _ := s.Forget(ctx, otherID)
	if err := s.Rollback(ctx, cp.ID); err != nil {
		t.Fatal(err)
	}
	if calls := nm.Calls(); calls[len(calls)-1] != "CheckpointRollback(/cp/1)" {
		t.Fatalf("calls %v", calls)
	}
	if s.Pending() != nil {
		t.Fatal("still pending after rollback")
	}
}

// NetworkManager rolls back by itself when the timeout passes; the service must stop claiming a
// pending change at that moment, and accept a new one.
func TestPendingExpires(t *testing.T) {
	s, _, _, clock := newService()
	s.Connect(ctx, savedID)
	clock.advance(15 * time.Second)
	if p := s.Pending(); p == nil || p.RollbackInS != 45 {
		t.Fatalf("pending %+v, want 45 s left", p)
	}
	clock.advance(45 * time.Second)
	if p := s.Pending(); p != nil {
		t.Fatalf("pending %+v after the rollback time", p)
	}
	if _, err := s.Forget(ctx, otherID); err != nil {
		t.Fatalf("a new change after expiry: %v", err)
	}
}

func TestFailedChangeRollsBackImmediately(t *testing.T) {
	s, nm, _, _ := newService()
	nm.err["ActivateWifi"] = errors.New("boom")
	if _, err := s.Connect(ctx, savedID); err == nil {
		t.Fatal("want the activation error")
	}
	if calls := nm.Calls(); calls[len(calls)-1] != "CheckpointRollback(/cp/1)" {
		t.Fatalf("calls %v", calls)
	}
	if s.Pending() != nil {
		t.Fatal("a failed change must not stay pending")
	}
}

func TestForgetUnderCheckpoint(t *testing.T) {
	s, nm, _, _ := newService()
	cp, err := s.Forget(ctx, otherID)
	if err != nil || cp.Kind != "wifi-forget" {
		t.Fatalf("%+v %v", cp, err)
	}
	want := []string{"Wifi", "CheckpointCreate(wifi,1m0s)", "DeleteConnection(" + otherID + ")"}
	if got := nm.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

func TestUpdateCellularUnderCheckpoint(t *testing.T) {
	s, nm, _, _ := newService()
	off := false
	cp, err := s.UpdateCellular(ctx, CellularUpdate{DataEnabled: &off})
	if err != nil || cp.Kind != "cellular" {
		t.Fatalf("%+v %v", cp, err)
	}
	want := []string{"CheckpointCreate(modem,1m0s)", "UpdateCellular(apn=-,data=false)"}
	if got := nm.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

func TestScanDedupesAndDropsHidden(t *testing.T) {
	s, nm, _, _ := newService()
	nm.scan = []VisibleNetwork{{"", 80, "wpa"}, {"home", 40, "wpa"}, {"home", 70, "wpa"}, {"cafe", 55, "open"}}
	got, err := s.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []VisibleNetwork{{"home", 70, "wpa"}, {"cafe", 55, "open"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCellularMergesModemAndData(t *testing.T) {
	s, nm, mm, _ := newService()
	mm.modem = ModemInfo{State: "connected", Operator: "MEO", AccessTech: "lte", Signal: 60, Registration: "home"}
	mm.sim = SIMInfo{Lock: "none", PINRetries: 3, PUKRetries: 10}
	nm.data = DataInfo{Enabled: true, State: "connected", APN: "internet", IP4: "10.1.2.3"}
	got, err := s.Cellular(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Modem != mm.modem || got.SIM != mm.sim || got.Data != nm.data {
		t.Fatalf("got %+v", got)
	}
}

// Forgetting the network the box is on cuts the owner off by design; the console does not offer it.
func TestForgetActiveNetworkIsRefused(t *testing.T) {
	s, nm, _, _ := newService()
	if _, err := s.Forget(ctx, savedID); !errors.Is(err, ErrActive) {
		t.Fatalf("want ErrActive, got %v", err)
	}
	for _, c := range nm.Calls() {
		if c != "Wifi" {
			t.Fatalf("nothing may change: %v", nm.Calls())
		}
	}
}
