package relay

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/caddyserver/certmagic"
)

func TestCertIsLetsEncryptByTLSALPNOnly(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "certmagic")
	c, err := NewCert(state, "127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	c.use()
	a := c.acme
	if !a.DisableHTTPChallenge || a.DisableTLSALPNChallenge || a.DNS01Solver != nil {
		t.Fatalf("challenges: http disabled %v, tls-alpn disabled %v, dns %v; want TLS-ALPN-01 only",
			a.DisableHTTPChallenge, a.DisableTLSALPNChallenge, a.DNS01Solver)
	}
	if a.CA != certmagic.LetsEncryptProductionCA || !a.Agreed {
		t.Fatalf("issuer: CA %q, agreed %v", a.CA, a.Agreed)
	}
	// No ACME contact: the owner's email would be in certmagic's logs and at Let's Encrypt.
	if a.Email != "" || certmagic.DefaultACME.Email != "" {
		t.Fatalf("ACME contact %q (default %q), want none", a.Email, certmagic.DefaultACME.Email)
	}
	// certmagic's challenge listener is the front's own address: it finds the port held and
	// binds nothing, never 443.
	if a.ListenHost != "127.0.0.1" || a.AltTLSALPNPort != 8443 {
		t.Fatalf("challenge listener %s:%d, want the front's 127.0.0.1:8443", a.ListenHost, a.AltTLSALPNPort)
	}
	if fs, ok := c.cfg.Storage.(*certmagic.FileStorage); !ok || fs.Path != dir {
		t.Fatalf("storage %#v, want file storage in %s", c.cfg.Storage, dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("certmagic's directory: %v, %v; want a 0700 directory", fi, err)
	}
	if len(c.cfg.Issuers) != 1 || c.cfg.Issuers[0] != a {
		t.Fatalf("issuers %v, want the one ACME issuer", c.cfg.Issuers)
	}
	if c.cfg.OnDemand != nil {
		t.Fatal("on-demand issuance is on: any SNI would ask the CA for a certificate")
	}
}

func TestCertRefusesABadFront(t *testing.T) {
	if _, err := NewCert(t.TempDir(), "8443"); err == nil {
		t.Fatal("NewCert with a front without a host: want an error")
	}
}

func TestCertRefusesAnEmptyOrRelativeStateDir(t *testing.T) {
	for _, dir := range []string{"", "state", "./state"} {
		if _, err := NewCert(dir, "127.0.0.1:8443"); err == nil {
			t.Errorf("NewCert(%q): want refused: certmagic would write keys relative to the working directory", dir)
		}
	}
}

func TestCertWithoutOneIsNotReady(t *testing.T) {
	c, err := NewCert(t.TempDir(), "127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	conn, peer := net.Pipe() // certmagic reads the hello's connection, as a handshake has one
	t.Cleanup(func() { conn.Close(); peer.Close() })
	hello := &tls.ClientHelloInfo{ServerName: "b1.box.test", Conn: conn}
	// Before anything is managed there is no certmagic at all: no certificate, no challenge.
	if c.Ready() {
		t.Fatal("Ready before anything was managed")
	}
	if _, err := c.ServerCert(hello); err == nil {
		t.Fatal("ServerCert answered before anything was managed")
	}
	if cfg, ok := c.ALPNChallenge(&tls.ClientHelloInfo{SupportedProtos: []string{"acme-tls/1"}}); ok || cfg != nil {
		t.Fatalf("ALPNChallenge before anything was managed = %v, %v", cfg, ok)
	}
	c.use()
	if c.Ready() {
		t.Fatal("Ready with nothing managed")
	}
	if _, err := c.ServerCert(hello); err == nil {
		t.Fatal("ServerCert answered a name it holds nothing for")
	}
	cfg, ok := c.ALPNChallenge(&tls.ClientHelloInfo{ServerName: "b1.box.test", SupportedProtos: []string{"acme-tls/1"}})
	if !ok || cfg == nil || cfg.GetCertificate == nil {
		t.Fatalf("ALPNChallenge = %v, %v; want certmagic's challenge config", cfg, ok)
	}
}

func TestCertForgetWipesItsStorage(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "certmagic")
	// relayd's own state beside certmagic's: Forget leaves it be.
	sibling := filepath.Join(state, "relay.db")
	if err := os.WriteFile(sibling, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := NewCert(state, "127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	c.use()
	// What certmagic keeps: the account and the certificate, under its own keys.
	for _, f := range []string{"acme/acme-v02.api.letsencrypt.org-directory/users/default/default.key",
		"certificates/acme-v02.api.letsencrypt.org-directory/b1.box.test/b1.box.test.key"} {
		if err := c.cfg.Storage.Store(context.Background(), f, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Forget(); err != nil {
		t.Fatal(err)
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("after Forget, %s holds %v", dir, left)
	}
	if c.cfg != nil || c.cache != nil {
		t.Fatal("after Forget certmagic is still configured: the next claim must start from nothing")
	}
	if b, err := os.ReadFile(sibling); err != nil || string(b) != "db" {
		t.Fatalf("Forget touched the state directory's relay.db: %q, %v", b, err)
	}
	if err := c.Forget(); err != nil {
		t.Fatalf("Forget again: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := c.Forget(); err != nil {
		t.Fatalf("Forget with certmagic's directory gone: %v", err)
	}
}

func TestCertIsBuiltOnceUntilForgotten(t *testing.T) {
	c, err := NewCert(t.TempDir(), "127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	c.use()
	first := c.cache
	c.use()
	if c.cache != first {
		t.Fatal("certmagic built again with nothing forgotten")
	}
	if err := c.Forget(); err != nil {
		t.Fatal(err)
	}
	c.use()
	if c.cache == first || c.cache == nil {
		t.Fatal("after Forget, the same certmagic")
	}
}
