package relay

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relay.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestIdentityIsStableAcrossReopen(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	a, err := s.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.BoxID == "" || a.State != Unclaimed {
		t.Fatalf("%+v", a)
	}
	want := Identity{BoxID: a.BoxID, State: Claimed, Email: "o@example.com", Name: "home", BoxDomain: "b.example.net", Relay: "relay.example.net"}
	if err := s.SetIdentity(ctx, want); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Identity(ctx)
	if err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestResetChangesBoxIDAndEmptiesDevices(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	a, _ := s.Identity(ctx)
	if err := s.SetIdentity(ctx, Identity{BoxID: a.BoxID, State: Claimed, Email: "o@example.com"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.AddDevice(ctx, Device{Serial: "01", Label: "phone", Issued: now, Expires: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Identity(ctx)
	if b.BoxID == a.BoxID || b.State != Unclaimed || b.Email != "" {
		t.Fatalf("%+v", b)
	}
	if ds, err := s.Devices(ctx); err != nil || len(ds) != 0 {
		t.Fatalf("%v %v", ds, err)
	}
}

func TestActiveSerial(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.AddDevice(ctx, Device{Serial: "0a", Label: "phone", Issued: now, Expires: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	ds, err := s.Devices(ctx)
	if err != nil || len(ds) != 1 || ds[0].Label != "phone" || !ds[0].Expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("%+v %v", ds, err)
	}
	if !s.ActiveSerial("0a", now) {
		t.Fatal("not active")
	}
	if s.ActiveSerial("0a", now.Add(time.Hour)) {
		t.Fatal("active after expiry")
	}
	if s.ActiveSerial("zz", now) {
		t.Fatal("unknown serial active")
	}
	if err := s.RemoveDevice(ctx, "0a"); err != nil {
		t.Fatal(err)
	}
	if s.ActiveSerial("0a", now) {
		t.Fatal("active after remove")
	}
}
