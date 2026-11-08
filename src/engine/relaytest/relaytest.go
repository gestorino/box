// Package relaytest stands in for the relay in tests: frps run in-process on loopback from frp's
// own packages, a fake control plane answering its hooks, the box's frp client, and a phone that
// dials by SNI. It touches no network but loopback. It is not internal: the relay's own repo
// tests its real frps configuration with StartWith.
package relaytest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	"github.com/fatedier/frp/pkg/policy/security"
	"github.com/fatedier/frp/server"

	"concierge/engine/internal/relay"
)

//go:embed frps-test.toml
var frpsTest string

// boxDomain is the stand-in relay's box domain: boxes are <name>.box.test.
const boxDomain = "box.test"

// Hooks are the fake control plane's answers to frps's hooks. NewProxy refuses every proxy type
// but https, and sets the subdomain to Subdomain's answer; CloseProxy is recorded (Closed).
type Hooks struct {
	// Login says whether a box with these metas may log in, and why not.
	Login func(metas map[string]string) (allow bool, reason string)
	// Subdomain is the box's name on the relay.
	Subdomain func(metas map[string]string) string
}

// Relay is a running stand-in relay.
type Relay struct {
	BoxAddr   string            // where boxes connect (frps's bindPort), 127.0.0.1:port
	PhoneAddr string            // where phones connect (frps's vhostHTTPSPort), 127.0.0.1:port
	BoxDomain string            // the relay's subDomainHost
	CACert    *x509.Certificate // the CA that signed the relay's certificate for relay.<BoxDomain>
	CAFile    string            // CACert as a PEM file, for frpc's trustedCaFile

	cp      *controlPlane
	cfgFile string
	ports   [2]int // bindPort, vhostHTTPSPort
	boxes   *atomic.Int64
	stop    func() // stops the running frps
}

// Start runs the stand-in relay with the contract's configuration (frps-test.toml).
func Start(t testing.TB, h Hooks) *Relay { return StartWith(t, h, frpsTest) }

// StartWith runs frps from template, a frps.toml whose placeholders {{BOX_DOMAIN}},
// {{CONTROL_PLANE}}, {{HOOK_SECRET}}, {{CERT_FILE}} and {{KEY_FILE}} it fills: the box domain,
// the fake control plane's URL and the secret its hook paths must carry, and a test CA's
// certificate and key for relay.<box-domain>. bindPort and vhostHTTPSPort move to loopback
// ports. It runs until the test's cleanup.
func StartWith(t testing.TB, h Hooks, template string) *Relay {
	t.Helper()
	if h.Login == nil || h.Subdomain == nil {
		t.Fatal("relaytest: Hooks needs Login and Subdomain")
	}
	dir := t.TempDir()
	ca, caFile, certFile, keyFile := writeRelayCert(t, dir, "relay."+boxDomain)

	cp := &controlPlane{hooks: h, secret: randomHex(t)}
	plane := httptest.NewServer(cp)
	t.Cleanup(plane.Close)

	rendered := strings.NewReplacer(
		"{{BOX_DOMAIN}}", boxDomain,
		"{{CONTROL_PLANE}}", plane.URL,
		"{{HOOK_SECRET}}", cp.secret,
		"{{CERT_FILE}}", certFile,
		"{{KEY_FILE}}", keyFile,
	).Replace(template)
	cfgFile := filepath.Join(dir, "frps.toml")
	if err := os.WriteFile(cfgFile, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &Relay{
		BoxDomain: boxDomain,
		CACert:    ca,
		CAFile:    caFile,
		cp:        cp,
		cfgFile:   cfgFile,
		ports:     [2]int{freePort(t), freePort(t)},
		boxes:     new(atomic.Int64),
	}
	r.BoxAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(r.ports[0]))
	r.PhoneAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(r.ports[1]))
	r.run(t)
	t.Cleanup(func() { r.stop() })
	return r
}

// Restart stops frps and runs it again on the same ports, as a relay restarted: boxes are
// dropped and must come back by themselves.
func (r *Relay) Restart(t testing.TB) {
	t.Helper()
	r.stop()
	r.run(t)
}

// Closed is the proxy names the relay told the control plane it closed, in order.
func (r *Relay) Closed() []string {
	r.cp.mu.Lock()
	defer r.cp.mu.Unlock()
	return slices.Clone(r.cp.closed)
}

// run loads the rendered configuration as frps does, and serves it.
func (r *Relay) run(t testing.TB) {
	t.Helper()
	cfg, _, err := config.LoadServerConfig(r.cfgFile, true)
	if err != nil {
		t.Fatalf("relaytest: frps configuration: %v", err)
	}
	cfg.BindAddr, cfg.ProxyBindAddr = "127.0.0.1", "127.0.0.1"
	cfg.BindPort, cfg.VhostHTTPSPort = r.ports[0], r.ports[1]
	if _, err := validation.NewConfigValidator(security.NewUnsafeFeatures(nil)).ValidateServerConfig(cfg); err != nil {
		t.Fatalf("relaytest: frps configuration: %v", err)
	}
	svr, err := server.NewService(cfg)
	if err != nil {
		t.Fatalf("relaytest: frps: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); svr.Run(ctx) }()
	r.stop = func() { cancel(); <-done }
}

// StartBox runs the box's frp client (relay.NewFrpClient) against r: TLS to BoxAddr verified by
// CAFile under relay.<BoxDomain>, with metas, and one https proxy to localAddr. It stops at the
// test's cleanup, or at stop.
func StartBox(t testing.TB, r *Relay, metas map[string]string, localAddr string) (stop func()) {
	t.Helper()
	svr, err := relay.NewFrpClient(relay.FrpConfig{
		Server:     r.BoxAddr,
		CAFile:     r.CAFile,
		ServerName: "relay." + r.BoxDomain,
		Metas:      metas,
		ProxyName:  fmt.Sprintf("box-%d", r.boxes.Add(1)),
		LocalAddr:  localAddr,
	})
	if err != nil {
		t.Fatalf("relaytest: box: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = svr.Run(ctx) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return stop
}

// DialPhone is a phone on the internet: TLS to PhoneAddr with SNI <name>.<BoxDomain>, trusting
// roots, presenting cert when it isn't nil. The handshake is the box's: the relay passes it
// through.
func DialPhone(t testing.TB, r *Relay, name string, roots *x509.CertPool, cert *tls.Certificate) (*tls.Conn, error) {
	t.Helper()
	cfg := &tls.Config{ServerName: name + "." + r.BoxDomain, RootCAs: roots}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", r.PhoneAddr, cfg)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { conn.Close() })
	return conn, nil
}

// controlPlane answers frps's httpPlugins (frp's doc/server_plugin.md), on paths carrying its
// secret.
type controlPlane struct {
	hooks  Hooks
	secret string

	mu     sync.Mutex
	closed []string
}

type hookAnswer struct {
	Reject       bool   `json:"reject"`
	RejectReason string `json:"reject_reason,omitempty"`
	Unchange     bool   `json:"unchange"`
	Content      any    `json:"content,omitempty"`
}

func (cp *controlPlane) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost || !slices.Contains(strings.Split(req.URL.Path, "/"), cp.secret) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var in struct {
		Op      string          `json:"op"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	answer, err := cp.answer(in.Op, in.Content)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer)
}

func (cp *controlPlane) answer(op string, content json.RawMessage) (hookAnswer, error) {
	allow := hookAnswer{Unchange: true}
	switch op {
	case "Login":
		var login struct {
			Metas map[string]string `json:"metas"`
		}
		if err := json.Unmarshal(content, &login); err != nil {
			return hookAnswer{}, err
		}
		if ok, reason := cp.hooks.Login(login.Metas); !ok {
			return hookAnswer{Reject: true, RejectReason: reason}, nil
		}
		return allow, nil
	case "NewProxy":
		var proxy struct {
			User struct {
				Metas map[string]string `json:"metas"`
			} `json:"user"`
			ProxyType string `json:"proxy_type"`
		}
		if err := json.Unmarshal(content, &proxy); err != nil {
			return hookAnswer{}, err
		}
		if proxy.ProxyType != "https" {
			return hookAnswer{Reject: true, RejectReason: "only https proxies"}, nil
		}
		// The whole content back, the subdomain the control plane's.
		var rewritten map[string]any
		dec := json.NewDecoder(strings.NewReader(string(content)))
		dec.UseNumber()
		if err := dec.Decode(&rewritten); err != nil {
			return hookAnswer{}, err
		}
		rewritten["subdomain"] = cp.hooks.Subdomain(proxy.User.Metas)
		return hookAnswer{Content: rewritten}, nil
	case "CloseProxy":
		var closed struct {
			ProxyName string `json:"proxy_name"`
		}
		if err := json.Unmarshal(content, &closed); err != nil {
			return hookAnswer{}, err
		}
		cp.mu.Lock()
		cp.closed = append(cp.closed, closed.ProxyName)
		cp.mu.Unlock()
		return allow, nil
	}
	return hookAnswer{}, fmt.Errorf("no hook %q", op)
}

// writeRelayCert writes a test CA and the relay's certificate for name, signed by it.
func writeRelayCert(t testing.TB, dir, name string) (ca *x509.Certificate, caFile, certFile, keyFile string) {
	t.Helper()
	caKey := newKey(t)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "relaytest CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	if ca, err = x509.ParseCertificate(caDER); err != nil {
		t.Fatal(err)
	}

	key := newKey(t)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	caFile = filepath.Join(dir, "ca.pem")
	certFile = filepath.Join(dir, "relay.pem")
	keyFile = filepath.Join(dir, "relay-key.pem")
	for path, block := range map[string]*pem.Block{
		caFile:   {Type: "CERTIFICATE", Bytes: caDER},
		certFile: {Type: "CERTIFICATE", Bytes: leafDER},
		keyFile:  {Type: "PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return ca, caFile, certFile, keyFile
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func randomHex(t testing.TB) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// freePort is a loopback port free a moment ago.
func freePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
