package relay_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"

	"concierge/engine/internal/relay"
	"concierge/engine/internal/secrets/secretstest"
	"concierge/engine/relaytest"
)

// fakeCert stands in for certmagic: it records what it was asked to manage.
type fakeCert struct {
	mu      sync.Mutex
	managed []string
	forgets int
	ready   atomic.Bool
}

func (f *fakeCert) Manage(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.managed = append(f.managed, name)
	return nil
}
func (f *fakeCert) Ready() bool { return f.ready.Load() }
func (f *fakeCert) Forget() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgets++
	return nil
}
func (f *fakeCert) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.managed...)
}

// tunnelRig is the stand-in relay, the box's front on loopback with a certificate for
// b1.<box-domain> injected, and the fake engine behind it.
type tunnelRig struct {
	relay    *relaytest.Relay
	ca       *relay.CA
	roots    *x509.CertPool
	front    string
	secret   string
	cert     *fakeCert
	refusals atomic.Int32
}

func newTunnelRig(t *testing.T) *tunnelRig {
	t.Helper()
	r := &tunnelRig{secret: "s3cr3t-" + strings.Repeat("x", 20), cert: &fakeCert{}}
	r.relay = relaytest.Start(t, relaytest.Hooks{
		Login: func(m map[string]string) (bool, string) {
			if m["box_id"] != "b1" || m["secret"] != r.secret {
				r.refusals.Add(1)
				return false, "unknown box"
			}
			return true, ""
		},
		Subdomain: func(m map[string]string) string { return m["box_id"] },
	})

	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, "engine via "+req.Header.Get("X-Concierge-Via"))
	}))
	t.Cleanup(engine.Close)

	ca, err := relay.LoadCA(context.Background(), secretstest.Start(t, "relay"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	r.ca = ca
	serverCert, leaf := serverCertFor(t, "b1."+r.relay.BoxDomain)
	r.roots = x509.NewCertPool()
	r.roots.AddCert(leaf)
	front := relay.NewFront(ca, func(string) bool { return true },
		func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &serverCert, nil },
		func(*tls.ClientHelloInfo) (*tls.Config, bool) { return nil, false },
		strings.TrimPrefix(engine.URL, "http://"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go front.Serve(ln)
	t.Cleanup(func() { front.Close() })
	r.front = ln.Addr().String()
	return r
}

func (r *tunnelRig) identity() relay.Identity {
	return relay.Identity{BoxID: "b1", State: relay.Claimed, Name: "b1",
		BoxDomain: r.relay.BoxDomain, Relay: r.relay.BoxAddr}
}

// phone asks the box for / as a phone on the internet would, through the relay.
func (r *tunnelRig) phone(t *testing.T, cert *tls.Certificate) (string, error) {
	t.Helper()
	conn, err := relaytest.DialPhone(t, r.relay, "b1", r.roots, cert)
	if err != nil {
		return "", err
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	req, _ := http.NewRequest("GET", "https://b1."+r.relay.BoxDomain+"/", nil)
	if err := req.Write(conn); err != nil {
		return "", err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func (r *tunnelRig) device(t *testing.T) tls.Certificate {
	t.Helper()
	_, p12, pw, _, err := r.ca.Issue("phone")
	if err != nil {
		t.Fatal(err)
	}
	key, leaf, chain, err := pkcs12.DecodeChain(p12, pw)
	if err != nil {
		t.Fatal(err)
	}
	c := tls.Certificate{PrivateKey: key, Leaf: leaf, Certificate: [][]byte{leaf.Raw}}
	for _, x := range chain {
		c.Certificate = append(c.Certificate, x.Raw)
	}
	return c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTunnelThroughRelay(t *testing.T) {
	r := newTunnelRig(t)
	tun := relay.NewFrpTunnel(r.relay.CAFile, r.front, r.cert)
	t.Cleanup(tun.Stop)

	if got := tun.Waiting(); got != "relay" {
		t.Fatalf("before Start, Waiting = %q, want relay", got)
	}
	if err := tun.Start(r.identity(), r.secret); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tunnel to connect", tun.Connected)
	if got := tun.Waiting(); got != "certificate" {
		t.Fatalf("connected without a certificate, Waiting = %q, want certificate", got)
	}
	// The certificate is managed for the box's name, once the relay is reached.
	waitFor(t, "the certificate to be managed", func() bool { return len(r.cert.names()) > 0 })
	if got := r.cert.names(); len(got) != 1 || got[0] != "b1."+r.relay.BoxDomain {
		t.Fatalf("managed %v, want [b1.%s]", got, r.relay.BoxDomain)
	}
	r.cert.ready.Store(true)
	if got := tun.Waiting(); got != "" {
		t.Fatalf("connected with a certificate, Waiting = %q, want empty", got)
	}

	// A device the box issued reaches the engine through the relay.
	dev := r.device(t)
	got, err := r.phone(t, &dev)
	if err != nil {
		t.Fatalf("phone with a device certificate: %v", err)
	}
	if got != "engine via relay" {
		t.Fatalf("phone got %q, want the engine's answer", got)
	}

	// Without a device certificate the box refuses it.
	if got, err := r.phone(t, nil); err == nil {
		t.Fatalf("phone without a device certificate got %q, want refused", got)
	}

	// Stopped: the relay has no way to the box.
	tun.Stop()
	tun.Stop() // idempotent
	if tun.Connected() {
		t.Fatal("Connected after Stop")
	}
	waitFor(t, "the relay to drop the box", func() bool {
		_, err := r.phone(t, &dev)
		return err != nil
	})

	// Start after Stop works.
	if err := tun.Start(r.identity(), r.secret); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tunnel to reconnect", tun.Connected)
	waitFor(t, "the phone to reach the box again", func() bool {
		got, err := r.phone(t, &dev)
		return err == nil && got == "engine via relay"
	})

	// Forget stops the tunnel first (no obtain under way), then wipes the certificate side.
	if err := tun.Forget(); err != nil {
		t.Fatal(err)
	}
	if tun.Connected() {
		t.Fatal("Connected after Forget: it must stop the tunnel before wiping")
	}
	if r.cert.forgets != 1 {
		t.Fatalf("Forget wiped the certificate %d times, want 1", r.cert.forgets)
	}
}

// captureHandler keeps every record at every level.
type captureHandler struct {
	mu    *sync.Mutex
	lines *[]string
}

func (h captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h captureHandler) Handle(_ context.Context, rec slog.Record) error {
	var b strings.Builder
	b.WriteString(rec.Message)
	rec.Attrs(func(a slog.Attr) bool { b.WriteString(" " + a.String()); return true })
	h.mu.Lock()
	*h.lines = append(*h.lines, b.String())
	h.mu.Unlock()
	return nil
}
func (h captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h captureHandler) WithGroup(string) slog.Handler      { return h }

func TestFrpLogsNeverCarryTheSecret(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	prev := slog.Default()
	slog.SetDefault(slog.New(captureHandler{&mu, &lines}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	relay.SetFrpLogLevel(t, slog.LevelDebug-4) // frp's trace

	r := newTunnelRig(t)
	// A box the relay refuses, then the box it lets in.
	refused := relay.NewFrpTunnel(r.relay.CAFile, r.front, &fakeCert{})
	t.Cleanup(refused.Stop)
	if err := refused.Start(r.identity(), "wrong-"+r.secret); err != nil {
		t.Fatal(err)
	}
	tun := relay.NewFrpTunnel(r.relay.CAFile, r.front, r.cert)
	t.Cleanup(tun.Stop)
	if err := tun.Start(r.identity(), r.secret); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tunnel to connect", tun.Connected)
	waitFor(t, "the relay to refuse the other box", func() bool { return r.refusals.Load() > 0 })
	dev := r.device(t)
	if _, err := r.phone(t, &dev); err != nil {
		t.Fatal(err)
	}
	refused.Stop()
	tun.Stop()

	mu.Lock()
	defer mu.Unlock()
	var frp, refusal int
	for _, l := range lines {
		if strings.Contains(l, r.secret) {
			t.Errorf("frp logged the box secret: %s", l)
		}
		if strings.HasPrefix(l, "frp: ") {
			frp++
		}
		if strings.Contains(l, "unknown box") {
			refusal++ // the refusal reached the log, and nothing of the metas with it
		}
	}
	if frp == 0 || refusal == 0 {
		t.Fatalf("frp's lines didn't reach slog (refusal logged: %d); captured %q", refusal, lines)
	}
}

// serverCertFor is the front's certificate for name, self-signed: what certmagic would hold.
func serverCertFor(t *testing.T, name string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}
