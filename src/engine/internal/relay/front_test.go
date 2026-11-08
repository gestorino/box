package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	pkcs12 "software.sslmate.com/src/go-pkcs12"

	"concierge/engine/internal/secrets/secretstest"
)

type frontRig struct {
	addr    string
	ca      *CA
	server  *x509.Certificate
	allowed sync.Map // serials Issue returned and not revoked
	engine  *httptest.Server
	acme    atomic.Pointer[tls.Config]
	front   *Front
}

func selfSigned(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "box.test"},
		DNSNames: []string{"box.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, leaf
}

func newFrontRig(t *testing.T) *frontRig {
	t.Helper()
	r := &frontRig{}
	ca, err := LoadCA(context.Background(), secretstest.Start(t, "relay"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	r.ca = ca
	mux := http.NewServeMux()
	mux.HandleFunc("/via", func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, strings.Join(req.Header.Values("X-Concierge-Via"), ","))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			typ, b, err := c.Read(req.Context())
			if err != nil {
				return
			}
			if c.Write(req.Context(), typ, b) != nil {
				return
			}
		}
	})
	r.engine = httptest.NewServer(mux)
	t.Cleanup(r.engine.Close)

	cert, leaf := selfSigned(t)
	r.server = leaf
	srv := NewFront(ca,
		func(serial string) bool { _, ok := r.allowed.Load(serial); return ok },
		func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil },
		func(h *tls.ClientHelloInfo) (*tls.Config, bool) { c := r.acme.Load(); return c, c != nil },
		strings.TrimPrefix(r.engine.URL, "http://"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.front = srv
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	r.addr = ln.Addr().String()
	return r
}

func (r *frontRig) device(t *testing.T, ca *CA) (tls.Certificate, string) {
	t.Helper()
	serial, p12, pw, _, err := ca.Issue("phone")
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
	if ca == r.ca {
		r.allowed.Store(serial, true)
	}
	return c, serial
}

func (r *frontRig) mustDevice(t *testing.T) tls.Certificate {
	c, _ := r.device(t, r.ca)
	return c
}

func (r *frontRig) client(certs ...tls.Certificate) *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(r.server)
	return &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{RootCAs: pool, ServerName: "box.test", Certificates: certs},
	}}
}

func (r *frontRig) get(c *http.Client, path string, hdr ...string) (string, error) {
	req, _ := http.NewRequest("GET", "https://"+r.addr+path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Add(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func TestFrontRefusesNoCertificate(t *testing.T) {
	r := newFrontRig(t)
	_, err := r.get(r.client(), "/via")
	if err == nil || !strings.Contains(err.Error(), "remote error: tls:") {
		t.Fatalf("want a TLS alert, got %v", err)
	}
}

func TestFrontRefusesAnotherCA(t *testing.T) {
	r := newFrontRig(t)
	other, err := LoadCA(context.Background(), secretstest.Start(t, "relay"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	oc, _ := r.device(t, other)
	_, err = r.get(r.client(oc), "/via")
	if err == nil || !strings.Contains(err.Error(), "remote error: tls:") {
		t.Fatalf("want a TLS alert, got %v", err)
	}
}

func TestFrontForwardsAndMarksVia(t *testing.T) {
	r := newFrontRig(t)
	got, err := r.get(r.client(r.mustDevice(t)), "/via", "X-Concierge-Via", "lan")
	if err != nil {
		t.Fatal(err)
	}
	if got != "relay" {
		t.Fatalf("engine saw X-Concierge-Via %q, want exactly relay", got)
	}
}

func TestFrontRevokedSerialCannotReturn(t *testing.T) {
	r := newFrontRig(t)
	cert, serial := r.device(t, r.ca)
	c := r.client(cert)
	c.Transport.(*http.Transport).TLSClientConfig.ClientSessionCache = tls.NewLRUClientSessionCache(8)
	if _, err := r.get(c, "/via"); err != nil {
		t.Fatal(err)
	}
	r.allowed.Delete(serial)
	// A new connection, with the session cache that would resume the old session.
	_, err := r.get(c, "/via")
	if err == nil || !strings.Contains(err.Error(), "remote error: tls:") {
		t.Fatalf("a revoked device got back in on a new connection: %v", err)
	}
}

func TestFrontRevokedSerialIsRefusedOnAnOpenConnection(t *testing.T) {
	r := newFrontRig(t)
	cert, serial := r.device(t, r.ca)
	c := r.client(cert)
	c.Transport.(*http.Transport).DisableKeepAlives = false
	status := func() int {
		resp, err := c.Get("https://" + r.addr + "/via")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if status() != 200 {
		t.Fatal("first request refused")
	}
	r.allowed.Delete(serial)
	if got := status(); got != http.StatusForbidden {
		t.Fatalf("revoked device on its keep-alive connection got %d, want 403", got)
	}
}

func TestFrontCloseDeviceClosesOnlyThatDevicesWebSockets(t *testing.T) {
	r := newFrontRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dial := func() (*websocket.Conn, string) {
		cert, serial := r.device(t, r.ca)
		ws, _, err := websocket.Dial(ctx, "wss://"+r.addr+"/ws", &websocket.DialOptions{HTTPClient: r.client(cert)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ws.CloseNow() })
		return ws, serial
	}
	a, serialA := dial()
	b, _ := dial()
	r.front.CloseDevice(serialA)
	if _, _, err := a.Read(ctx); err == nil {
		t.Fatal("the closed device's WebSocket still reads")
	}
	if err := b.Write(ctx, websocket.MessageText, []byte("still")); err != nil {
		t.Fatal(err)
	}
	if _, m, err := b.Read(ctx); err != nil || string(m) != "still" {
		t.Fatalf("other device's WebSocket: %q %v", m, err)
	}
}

func TestFrontPassesWebSockets(t *testing.T) {
	r := newFrontRig(t)
	c := r.client(r.mustDevice(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "wss://"+r.addr+"/ws", &websocket.DialOptions{HTTPClient: c})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if err := ws.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, b, err := ws.Read(ctx)
	if err != nil || string(b) != "hello" {
		t.Fatalf("echo %q %v", b, err)
	}
}

func TestFrontAnswersTheACMEChallengeWithoutAClientCertificate(t *testing.T) {
	r := newFrontRig(t)
	cert, _ := selfSigned(t)
	r.acme.Store(&tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"acme-tls/1"}})
	conn, err := tls.Dial("tcp", r.addr, &tls.Config{
		InsecureSkipVerify: true, NextProtos: []string{"acme-tls/1"}, ServerName: "box.test"})
	if err != nil {
		t.Fatalf("challenge handshake: %v", err)
	}
	defer conn.Close()
	if p := conn.ConnectionState().NegotiatedProtocol; p != "acme-tls/1" {
		t.Fatalf("negotiated %q", p)
	}
}

func TestFrontACMEConfigCannotCarryTraffic(t *testing.T) {
	r := newFrontRig(t)
	cert, _ := selfSigned(t)
	// A challenge config that also speaks http/1.1, and a hello offering both.
	r.acme.Store(&tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1", "acme-tls/1"}})
	conn, err := tls.Dial("tcp", r.addr, &tls.Config{
		InsecureSkipVerify: true, NextProtos: []string{"http/1.1", "acme-tls/1"}, ServerName: "box.test"})
	if err == nil {
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		io.WriteString(conn, "GET /via HTTP/1.1\r\nHost: box.test\r\n\r\n")
		b := make([]byte, 64)
		n, rerr := conn.Read(b)
		if rerr == nil || strings.Contains(string(b[:n]), "HTTP/1.1") {
			t.Fatalf("an HTTP response without a client certificate: %q %v", b[:n], rerr)
		}
	}
}
