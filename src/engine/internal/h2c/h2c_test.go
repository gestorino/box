package h2c_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"concierge/engine/internal/h2c"
)

// serve answers with the request's protocol on a Unix socket, as a service does.
func serve(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "h2c") // short: macOS caps socket paths at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.Proto) }),
		Protocols:         h2c.Protocols(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return socket
}

func TestAServiceSpeaksHTTP2(t *testing.T) {
	socket := serve(t)
	c := &http.Client{Transport: h2c.Transport(socket), Timeout: 5 * time.Second}
	resp, err := c.Get("http://connector/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "HTTP/2.0" {
		t.Fatalf("got %d %q, want 200 HTTP/2.0", resp.StatusCode, body)
	}
}

func TestAServiceRefusesHTTP1(t *testing.T) {
	socket := serve(t)
	plain := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	resp, err := plain.Get("http://connector/ping")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("an HTTP/1.1 request was answered %d", resp.StatusCode)
	}
}
