package signins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "si")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "signins.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestAccountsAndJournal(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	if _, ok, err := s.Account(ctx, shopKey); ok || err != nil {
		t.Fatalf("empty account = %v %v", ok, err)
	}
	at := time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
	a := Account{Key: shopKey, Email: "a@b.c", Owner: map[string]string{"name": "N"}, State: "warn", Run: 4, CreatedAt: at}
	if err := s.PutAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.State, a.Reason, a.Run = "registered", "", 7
	if err := s.PutAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Account(ctx, shopKey)
	if err != nil || !ok || got.State != "registered" || got.Run != 7 || got.Owner["name"] != "N" || !got.CreatedAt.Equal(at) {
		t.Fatalf("account = %+v %v %v", got, ok, err)
	}
	if err := s.DeleteAccount(ctx, shopKey); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Account(ctx, shopKey); ok {
		t.Fatal("account should be gone")
	}

	for _, ev := range []string{"registering", "registered", "removed", "registering"} {
		if err := s.Journal(ctx, shopKey, ev, "a@b.c", ""); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Registrations(ctx, shopKey); err != nil || n != 2 {
		t.Fatalf("registrations = %d %v", n, err)
	}
}

func TestTheStoreFileIsOwnerOnly(t *testing.T) {
	_, path := openTestStore(t)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m != 0o600 {
		t.Fatalf("mode = %o", m)
	}
}
