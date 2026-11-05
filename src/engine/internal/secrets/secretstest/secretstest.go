// Package secretstest serves a real secretd (Store and Handler) on a Unix socket for tests.
package secretstest

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"concierge/engine/internal/h2c"
	"concierge/engine/internal/secrets"
)

// Start serves secretd under a short temp dir (macOS caps socket paths at 104 bytes), the test
// process's uid its caller ns, and returns that caller's client. It closes on cleanup.
func Start(t testing.TB, ns string) *secrets.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := secrets.OpenStore(filepath.Join(dir, "secrets.db"), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: secrets.NewHandler(st, map[uint32]string{uint32(os.Getuid()): ns}),
		ConnContext: secrets.ConnContext, Protocols: h2c.Protocols()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return secrets.NewClient(sock)
}

// Down is a client whose secretd is not running: nobody listens on its socket.
func Down(t testing.TB) *secrets.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return secrets.NewClient(filepath.Join(dir, "s"))
}
