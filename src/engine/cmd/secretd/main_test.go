package main

import (
	"context"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"concierge/engine/internal/secrets"
)

func me(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return u.Username
}

func keyFile(t *testing.T, n int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(p, []byte(strings.Repeat("k", n)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func sockDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func args(dir, key string, extra ...string) []string {
	return append([]string{"-dir", dir, "-master-key-file", key, "-listen", filepath.Join(dir, "s")}, extra...)
}

func TestRefusesShortKeyFile(t *testing.T) {
	dir := sockDir(t)
	err := run(context.Background(), args(dir, keyFile(t, 31), "-caller", "engine="+me(t)))
	if err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("short: %v", err)
	}
	err = run(context.Background(), args(dir, keyFile(t, 33), "-caller", "engine="+me(t)))
	if err == nil || !strings.Contains(err.Error(), "32 bytes") || strings.Contains(err.Error(), "kkkk") {
		t.Fatalf("long: %v", err)
	}
}

func TestUnknownCallerUser(t *testing.T) {
	dir := sockDir(t)
	err := run(context.Background(), args(dir, keyFile(t, 32), "-caller", "engine=no-such-user-xyzzy"))
	if err == nil || !strings.Contains(err.Error(), "no-such-user-xyzzy") {
		t.Fatalf("%v", err)
	}
}

func TestRefusedCallers(t *testing.T) {
	me := me(t)
	for name, callers := range map[string][]string{
		"none":      nil,
		"malformed": {"-caller", "engine"},
		"duplicate": {"-caller", "engine=" + me, "-caller", "calld=" + me},
		"unknown":   {"-caller", "enginee=" + me},
	} {
		dir := sockDir(t)
		if err := run(context.Background(), args(dir, keyFile(t, 32), callers...)); err == nil {
			t.Errorf("%s: started", name)
		}
	}
}

// root is never a caller: uid 0 as a caller would make every root process one.
func TestRefusesRootAsACaller(t *testing.T) {
	dir := sockDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // started anyway, it stops at once
	err := run(ctx, args(dir, keyFile(t, 32), "-caller", "engine=root"))
	if err == nil || !strings.Contains(err.Error(), "uid 0") {
		t.Fatalf("%v", err)
	}
}

// relayd is a caller of its own: its namespace is "relay".
func TestServesRelayAsACaller(t *testing.T) {
	dir := sockDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, args(dir, keyFile(t, 32), "-caller", "relay="+me(t))) }()
	sock := filepath.Join(dir, "s")
	for i := 0; ; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		if i > 200 {
			t.Fatal("never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServesItsCallers(t *testing.T) {
	dir := sockDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, args(dir, keyFile(t, 32), "-caller", "engine="+me(t))) }()
	sock := filepath.Join(dir, "s")
	for i := 0; ; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		if i > 200 {
			t.Fatal("never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c := secrets.NewClient(sock)
	if err := c.Put(ctx, "openrouter", "k"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := c.Get(ctx, "openrouter"); v != "k" || !ok || err != nil {
		t.Fatalf("%q %v %v", v, ok, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.db")); err != nil {
		t.Fatal(err)
	}
}
