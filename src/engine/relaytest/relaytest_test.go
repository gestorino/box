package relaytest_test

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
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"

	"concierge/engine/internal/relay"
	"concierge/engine/relaytest"
)

// byBoxID is the control plane the tests stand in: a box is known by its box_id, and its name
// on the relay is that id.
func byBoxID(known ...string) relaytest.Hooks {
	return relaytest.Hooks{
		Login: func(metas map[string]string) (bool, string) {
			for _, k := range known {
				if metas["box_id"] == k {
					return true, ""
				}
			}
			return false, "unknown box"
		},
		Subdomain: func(metas map[string]string) string { return metas["box_id"] },
	}
}

// echoBox is a box's local TLS server: a certificate for <name>.<box-domain> only it holds, and
// an echo. It returns its address and the roots a phone trusts it by.
func echoBox(t *testing.T, r *relaytest.Relay, name string) (string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host := name + "." + r.BoxDomain
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String(), roots
}

// echoes says whether a phone dialling name reaches the box that trusts by roots, and its echo.
func echoes(t *testing.T, r *relaytest.Relay, name string, roots *x509.CertPool) bool {
	t.Helper()
	conn, err := relaytest.DialPhone(t, r, name, roots, nil)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping " + name + "\n")); err != nil {
		return false
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	return err == nil && line == "ping "+name+"\n"
}

// eventually waits up to 30 s (frp's reconnect backoff) for ok.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// closesWithoutData says a phone dialling name gets no handshake and no byte from any box: frps
// answers a name it routes nowhere with its own unrecognized_name alert, and closes.
func closesWithoutData(t *testing.T, r *relaytest.Relay, name string) {
	t.Helper()
	conn, err := relaytest.DialPhone(t, r, name, x509.NewCertPool(), nil)
	if err == nil {
		conn.Close()
		t.Fatalf("a phone dialling %q completed a handshake", name)
	}
	if !strings.Contains(err.Error(), "unrecognized name") {
		t.Fatalf("a phone dialling %q: %v, want the relay's unrecognized_name", name, err)
	}
}

func isClosed(err error) bool {
	s := err.Error()
	return err == io.EOF || strings.Contains(s, "EOF") || strings.Contains(s, "connection reset")
}

func TestRelayRoutesBySNI(t *testing.T) {
	r := relaytest.Start(t, byBoxID("b1", "b2"))
	addr1, roots1 := echoBox(t, r, "b1")
	addr2, roots2 := echoBox(t, r, "b2")
	relaytest.StartBox(t, r, map[string]string{"box_id": "b1", "secret": "s1"}, addr1)
	relaytest.StartBox(t, r, map[string]string{"box_id": "b2", "secret": "s2"}, addr2)

	eventually(t, "b1 reachable", func() bool { return echoes(t, r, "b1", roots1) })
	eventually(t, "b2 reachable", func() bool { return echoes(t, r, "b2", roots2) })
	// b1's name reaches b1's server only: b2's roots don't trust what answers there.
	if echoes(t, r, "b1", roots2) {
		t.Fatal("b1's name reached b2")
	}
	closesWithoutData(t, r, "nobody")
}

func TestRelayRefusesUnknownBox(t *testing.T) {
	refused := make(chan struct{}, 1)
	hooks := byBoxID("b1")
	login := hooks.Login
	hooks.Login = func(metas map[string]string) (bool, string) {
		ok, reason := login(metas)
		if !ok {
			select {
			case refused <- struct{}{}:
			default:
			}
		}
		return ok, reason
	}
	r := relaytest.Start(t, hooks)
	addr, roots := echoBox(t, r, "intruder")
	relaytest.StartBox(t, r, map[string]string{"box_id": "intruder", "secret": "s"}, addr)

	select {
	case <-refused:
	case <-time.After(10 * time.Second):
		t.Fatal("the relay never asked the control plane about the box")
	}
	if echoes(t, r, "intruder", roots) {
		t.Fatal("a box the control plane refused at Login is reachable")
	}
	closesWithoutData(t, r, "intruder")
}

func TestRelayRewritesTheName(t *testing.T) {
	// Every box asks the relay for the same subdomain; the control plane gives each its own.
	r := relaytest.Start(t, byBoxID("b1", "b2"))
	addr1, roots1 := echoBox(t, r, "b1")
	addr2, roots2 := echoBox(t, r, "b2")
	relaytest.StartBox(t, r, map[string]string{"box_id": "b1"}, addr1)
	relaytest.StartBox(t, r, map[string]string{"box_id": "b2"}, addr2)

	eventually(t, "b1 under its own name", func() bool { return echoes(t, r, "b1", roots1) })
	eventually(t, "b2 under its own name", func() bool { return echoes(t, r, "b2", roots2) })
	closesWithoutData(t, r, relay.RequestedSubdomain)
}

func TestRelayRefusesTCP(t *testing.T) {
	r := relaytest.Start(t, byBoxID("b1"))
	port := freePort(t)

	// A box asking for a TCP port on the relay, frpc built by hand: the box's own client makes
	// only an https proxy.
	host, p, _ := net.SplitHostPort(r.BoxAddr)
	serverPort, _ := strconv.Atoi(p)
	common := &v1.ClientCommonConfig{
		ServerAddr:    host,
		ServerPort:    serverPort,
		LoginFailExit: new(false),
		Metadatas:     map[string]string{"box_id": "b1"},
	}
	common.Transport.TLS.TrustedCaFile = r.CAFile
	common.Transport.TLS.ServerName = "relay." + r.BoxDomain
	tcp := &v1.TCPProxyConfig{
		ProxyBaseConfig: v1.ProxyBaseConfig{
			Name:         "tcp",
			Type:         "tcp",
			ProxyBackend: v1.ProxyBackend{LocalIP: "127.0.0.1", LocalPort: 1},
		},
		RemotePort: port,
	}
	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll([]v1.ProxyConfigurer{tcp}, nil); err != nil {
		t.Fatal(err)
	}
	svr, err := client.NewService(client.ServiceOptions{
		Common:                 common,
		ConfigSourceAggregator: source.NewAggregator(configSource),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = svr.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	eventually(t, "the TCP proxy refused", func() bool {
		st, ok := svr.StatusExporter().GetProxyStatus("tcp")
		return ok && st.Phase == "start error" && strings.Contains(st.Err, "only https")
	})
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second); err == nil {
		c.Close()
		t.Fatalf("the relay listens on the port a box asked for")
	}
}

func TestRelayRestart(t *testing.T) {
	r := relaytest.Start(t, byBoxID("b1"))
	addr, roots := echoBox(t, r, "b1")
	relaytest.StartBox(t, r, map[string]string{"box_id": "b1"}, addr)
	eventually(t, "b1 reachable", func() bool { return echoes(t, r, "b1", roots) })

	open, err := relaytest.DialPhone(t, r, "b1", roots, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Restart(t)

	// The old relay is gone: the phone's connection through it is closed.
	_ = open.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := open.Read(make([]byte, 1)); err == nil || !isClosed(err) {
		t.Fatalf("a connection through the stopped relay: %v, want it closed", err)
	}
	eventually(t, "b1 back by itself", func() bool { return echoes(t, r, "b1", roots) })
	if got := r.Closed(); len(got) == 0 {
		t.Fatal("the relay's stop told the control plane of no closed proxy")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
