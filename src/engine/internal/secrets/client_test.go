package secrets

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concierge/engine/internal/h2c"
)

func clientFor(t *testing.T, callers map[uint32]string) (*Client, *http.Server) {
	t.Helper()
	st, _ := open(t, key(1))
	dir, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: NewHandler(st, callers), ConnContext: ConnContext, Protocols: h2c.Protocols()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return NewClient(sock), srv
}

func TestClientRoundTrip(t *testing.T) {
	c, _ := clientFor(t, us())
	v, ok, err := c.Get(ctx, "openrouter")
	if v != "" || ok || err != nil {
		t.Fatalf("absent: %q %v %v", v, ok, err)
	}
	if err := c.Put(ctx, "signins/a.com/password", "pw"); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ctx, "openrouter", "k"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := c.Get(ctx, "signins/a.com/password"); v != "pw" || !ok || err != nil {
		t.Fatalf("get: %q %v %v", v, ok, err)
	}
	if err := c.Delete(ctx, "openrouter"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Get(ctx, "openrouter"); ok {
		t.Fatal("still there")
	}
}

func TestClientServerDownIsUnavailable(t *testing.T) {
	c, srv := clientFor(t, us())
	srv.Close()
	if _, _, err := c.Get(ctx, "openrouter"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("get: %v", err)
	}
	if err := c.Put(ctx, "openrouter", "k"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("put: %v", err)
	}
}

func TestClientNoSocketIsUnavailable(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "none"))
	if _, _, err := c.Get(ctx, "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

// A caller secretd refuses can't have its secrets: to the owner that is "secrets unavailable".
func TestClientNotACallerIsUnavailable(t *testing.T) {
	c, _ := clientFor(t, map[uint32]string{uint32(os.Getuid()) + 1: "engine"})
	err := c.Put(context.Background(), "openrouter", "sekrit-value")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "openrouter") || strings.Contains(err.Error(), "sekrit-value") {
		t.Fatalf("error: %v", err)
	}
	if _, _, err := c.Get(context.Background(), "openrouter"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("get: %v", err)
	}
}

// Any answer secretd isn't meant to give (here 409) is unavailable too, never a value in the error.
func TestClientUnexpectedStatusIsUnavailable(t *testing.T) {
	dir, _ := os.MkdirTemp("", "sd")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, _ := net.Listen("unix", sock)
	srv := &http.Server{Protocols: h2c.Protocols(), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"conflict"}`, http.StatusConflict)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	c := NewClient(sock)
	if _, _, err := c.Get(ctx, "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("get: %v", err)
	}
	if err := c.Put(ctx, "x", "sekrit-value"); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "sekrit-value") {
		t.Fatalf("put: %v", err)
	}
	if err := c.Delete(ctx, "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("delete: %v", err)
	}
}

func TestClientBadNameIsAPlainError(t *testing.T) {
	c, _ := clientFor(t, us())
	err := c.Put(ctx, "Bad Name", "v")
	if err == nil || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "400") {
		t.Fatalf("%v", err)
	}
}

// The largest value there is, of the characters JSON escapes six times longer, goes there and back.
func TestClientCarriesTheLargestValue(t *testing.T) {
	c, _ := clientFor(t, us())
	v := strings.Repeat("<>&", maxValueBytes/3) + "<"
	if len(v) != maxValueBytes {
		t.Fatalf("len %d", len(v))
	}
	if err := c.Put(ctx, "big", v); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := c.Get(ctx, "big"); got != v || !ok || err != nil {
		t.Fatalf("get: %d bytes %v %v", len(got), ok, err)
	}
}

func TestClientServerErrorIsUnavailable(t *testing.T) {
	dir, _ := os.MkdirTemp("", "sd")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, _ := net.Listen("unix", sock)
	srv := &http.Server{Protocols: h2c.Protocols(), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"secrets store failed"}`, 500)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	if _, _, err := NewClient(sock).Get(ctx, "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}
