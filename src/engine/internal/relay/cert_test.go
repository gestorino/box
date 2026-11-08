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
	dir := t.TempDir()
	c, err := NewCert(dir, "owner@example.org", "127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	a := c.acme
	if !a.DisableHTTPChallenge || a.DisableTLSALPNChallenge || a.DNS01Solver != nil {
		t.Fatalf("challenges: http disabled %v, tls-alpn disabled %v, dns %v; want TLS-ALPN-01 only",
			a.DisableHTTPChallenge, a.DisableTLSALPNChallenge, a.DNS01Solver)
	}
	if a.CA != certmagic.LetsEncryptProductionCA || !a.Agreed || a.Email != "owner@example.org" {
		t.Fatalf("issuer: CA %q, agreed %v, email %q", a.CA, a.Agreed, a.Email)
	}
	// certmagic's challenge listener is the front's own address: it finds the port held and
	// binds nothing, never 443.
	if a.ListenHost != "127.0.0.1" || a.AltTLSALPNPort != 8443 {
		t.Fatalf("challenge listener %s:%d, want the front's 127.0.0.1:8443", a.ListenHost, a.AltTLSALPNPort)
	}
	if fs, ok := c.cfg.Storage.(*certmagic.FileStorage); !ok || fs.Path != dir {
		t.Fatalf("storage %#v, want file storage in %s", c.cfg.Storage, dir)
	}
	if len(c.cfg.Issuers) != 1 || c.cfg.Issuers[0] != a {
		t.Fatalf("issuers %v, want the one ACME issuer", c.cfg.Issuers)
	}
	if c.cfg.OnDemand != nil {
		t.Fatal("on-demand issuance is on: any SNI would ask the CA for a certificate")
	}
}

func TestCertRefusesABadFront(t *testing.T) {
	if _, err := NewCert(t.TempDir(), "owner@example.org", "8443"); err == nil {
		t.Fatal("NewCert with a front without a host: want an error")
	}
}

func TestCertWithoutOneIsNotReady(t *testing.T) {
	c, err := NewCert(t.TempDir(), "owner@example.org", "127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	if c.Ready() {
		t.Fatal("Ready with nothing managed")
	}
	conn, peer := net.Pipe() // certmagic reads the hello's connection, as a handshake has one
	t.Cleanup(func() { conn.Close(); peer.Close() })
	if _, err := c.ServerCert(&tls.ClientHelloInfo{ServerName: "b1.box.test", Conn: conn}); err == nil {
		t.Fatal("ServerCert answered a name it holds nothing for")
	}
	cfg, ok := c.ALPNChallenge(&tls.ClientHelloInfo{ServerName: "b1.box.test", SupportedProtos: []string{"acme-tls/1"}})
	if !ok || cfg == nil || cfg.GetCertificate == nil {
		t.Fatalf("ALPNChallenge = %v, %v; want certmagic's challenge config", cfg, ok)
	}
}

func TestCertForgetWipesItsStorage(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCert(dir, "owner@example.org", "127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	// What certmagic keeps: the account and the certificate, under its own keys.
	for _, f := range []string{"acme/acme-v02.api.letsencrypt.org-directory/users/owner@example.org/owner.key",
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
	if err := c.Forget(); err != nil {
		t.Fatalf("Forget again: %v", err)
	}
	gone, err := NewCert(filepath.Join(dir, "never-made"), "owner@example.org", "127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	if err := gone.Forget(); err != nil {
		t.Fatalf("Forget with no storage yet: %v", err)
	}
}
