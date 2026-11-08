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
	revoked atomic.Bool
	serial  atomic.Value
	engine  *httptest.Server
	acme    *tls.Config
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
		func(serial string) bool { return !r.revoked.Load() },
		func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil },
		func(h *tls.ClientHelloInfo) (*tls.Config, bool) { return r.acme, r.acme != nil },
		strings.TrimPrefix(r.engine.URL, "http://"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	r.addr = ln.Addr().String()
	return r
}

func (r *frontRig) device(t *testing.T, ca *CA) tls.Certificate {
	t.Helper()
	_, p12, pw, _, err := ca.Issue("phone")
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
	if _, err := r.get(r.client(), "/via"); err == nil {
		t.Fatal("a client with no certificate got through")
	}
}

func TestFrontRefusesAnotherCA(t *testing.T) {
	r := newFrontRig(t)
	other, err := LoadCA(context.Background(), secretstest.Start(t, "relay"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.get(r.client(r.device(t, other)), "/via"); err == nil {
		t.Fatal("a certificate from another CA got through")
	}
}

func TestFrontForwardsAndMarksVia(t *testing.T) {
	r := newFrontRig(t)
	got, err := r.get(r.client(r.device(t, r.ca)), "/via", "X-Concierge-Via", "lan")
	if err != nil {
		t.Fatal(err)
	}
	if got != "relay" {
		t.Fatalf("engine saw X-Concierge-Via %q, want exactly relay", got)
	}
}

func TestFrontRevokedSerialCannotReturn(t *testing.T) {
	r := newFrontRig(t)
	c := r.client(r.device(t, r.ca))
	c.Transport.(*http.Transport).TLSClientConfig.ClientSessionCache = tls.NewLRUClientSessionCache(8)
	if _, err := r.get(c, "/via"); err != nil {
		t.Fatal(err)
	}
	r.revoked.Store(true)
	// A new connection, with the session cache that would resume the old session.
	if _, err := r.get(c, "/via"); err == nil {
		t.Fatal("a revoked device got back in on a new connection")
	}
}

func TestFrontPassesWebSockets(t *testing.T) {
	r := newFrontRig(t)
	c := r.client(r.device(t, r.ca))
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
	r.acme = &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"acme-tls/1"}}
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
