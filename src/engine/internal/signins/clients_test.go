package signins

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"concierge/engine/internal/h2c"
)

// serveUnix serves h as a daemon does, unencrypted HTTP/2 only, on a Unix socket under a short temp
// dir (macOS caps socket paths at 104 bytes).
func serveUnix(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, Protocols: h2c.Protocols()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return socket
}

func TestMailClientGivesTheAddressOnlyWhenConnected(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"state":"connected","address":"a@gmail.com"}`, "a@gmail.com"},
		{`{"state":"paused_mobile","address":"a@gmail.com","copied":3}`, "a@gmail.com"},
		{`{"state":"disconnected","address":"a@gmail.com"}`, ""},
		{`{"state":"auth_failed","address":"a@gmail.com"}`, ""},
		{`{"state":"disconnected"}`, ""},
		{`{"state":"connecting"}`, ""},
	} {
		socket := serveUnix(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/email/status" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(c.body))
		})
		got, err := NewMailClient(socket).Address(context.Background())
		if err != nil || got != c.want {
			t.Errorf("%s: address = %q, %v", c.body, got, err)
		}
	}
}

func TestHostdClientOwner(t *testing.T) {
	socket := serveUnix(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uplink":
			w.Write([]byte(`{"uplink":"mobile"}`))
		case "/settings/account":
			w.Write([]byte(`{"owner_first_name":"Alex","owner_last_name":"Sample","phone":"+351912345678","address":{"city":"Lisboa"},"card":null}`))
		default:
			http.NotFound(w, r)
		}
	})
	host := NewHostdClient(socket)
	if got, err := host.Uplink(context.Background()); err != nil || got != "mobile" {
		t.Fatalf("uplink = %q, %v", got, err)
	}
	want := Owner{FirstName: "Alex", LastName: "Sample", Phone: "+351912345678"}
	if got, err := host.Owner(context.Background()); err != nil || got != want {
		t.Fatalf("owner = %+v, %v", got, err)
	}
}

func TestClientsFailWhenTheDaemonIsAway(t *testing.T) {
	dir, _ := os.MkdirTemp("", "sc")
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "none.sock")
	if _, err := NewHostdClient(socket).Uplink(context.Background()); err == nil {
		t.Fatal("uplink: no error")
	}
	if _, err := NewHostdClient(socket).Owner(context.Background()); err == nil {
		t.Fatal("owner: no error")
	}
	if _, err := NewMailClient(socket).Address(context.Background()); err == nil {
		t.Fatal("mail: no error")
	}
}
