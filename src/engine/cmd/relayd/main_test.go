package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"concierge/engine/internal/h2c"
)

func TestTheDefaultsAreTheService(t *testing.T) {
	if defaultControlPlane != "https://gestorino.com" || defaultBoxDomain != "gestorino.net" {
		t.Fatalf("defaults %q %q", defaultControlPlane, defaultBoxDomain)
	}
	if err := checkDomains(defaultControlPlane, defaultBoxDomain); err != nil {
		t.Fatalf("the defaults refused: %v", err)
	}
}

func TestCheckDomains(t *testing.T) {
	for _, tc := range []struct{ cp, bd string }{
		{"https://gestorino.com/", "gestorino.net"},
		{"https://cp.example.org", "box.example.net"},
	} {
		if err := checkDomains(tc.cp, tc.bd); err != nil {
			t.Errorf("%q %q refused: %v", tc.cp, tc.bd, err)
		}
	}
	for _, cp := range []string{
		"http://gestorino.com", "http://127.0.0.1:8080", "gestorino.com", "https://", "ftp://gestorino.com",
		"https://u:p@gestorino.com", "https://gestorino.com/box", "https://gestorino.com?x=1", "https://gestorino.com#x", "",
	} {
		if err := checkDomains(cp, "gestorino.net"); err == nil {
			t.Errorf("control plane %q accepted", cp)
		}
	}
	for _, bd := range []string{
		"", "gestorino.net.", "Gestorino.net", "GESTORINO.NET", "gestorino", "gestorino..net", ".gestorino.net",
		"-gestorino.net", "gestorino-.net", "gestorino.net:7000", "gestorino_x.net", "https://gestorino.net", "gestorino.net/x",
	} {
		if err := checkDomains("https://gestorino.com", bd); err == nil {
			t.Errorf("box domain %q accepted", bd)
		}
	}
}

// relayd closes its store only after the socket's requests are through: serveUntil returns once
// every handler has, so a claim the control plane accepted is stored before anything stops.
func TestServeUntilDrainsBeforeReturning(t *testing.T) {
	dir, err := os.MkdirTemp("", "rd") // short: macOS caps socket paths at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	inside, release := make(chan struct{}), make(chan struct{})
	var finished atomic.Bool
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(inside)
		<-release
		finished.Store(true)
	}), Protocols: h2c.Protocols()}
	ctx, stop := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { returned <- serveUntil(ctx, srv, ln, 10*time.Second) }()

	c := &http.Client{Transport: h2c.Transport(sock)}
	go func() {
		if resp, err := c.Get("http://relayd/x"); err == nil {
			resp.Body.Close()
		}
	}()
	<-inside
	stop()
	select {
	case err := <-returned:
		t.Fatalf("returned (%v) with a request still being handled", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never returned")
	}
	if !finished.Load() {
		t.Fatal("returned before the handler finished")
	}
}
