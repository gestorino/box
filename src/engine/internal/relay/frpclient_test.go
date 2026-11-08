package relay_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"concierge/engine/internal/relay"
	"concierge/engine/relaytest"
)

func TestFrpClientNeedsACAFile(t *testing.T) {
	// Without a CA file frp doesn't verify the relay at all, and the box's secret would go to
	// whoever answers.
	_, err := relay.NewFrpClient(relay.FrpConfig{
		Server:     "127.0.0.1:7000",
		ServerName: "relay.box.test",
		ProxyName:  "b1",
		LocalAddr:  "127.0.0.1:8443",
	})
	if err == nil || !strings.Contains(err.Error(), "CA file") {
		t.Fatalf("NewFrpClient without a CA file: %v, want refused", err)
	}
}

func TestFrpClientVerifiesTheRelay(t *testing.T) {
	logins := make(chan string, 16)
	r := relaytest.Start(t, relaytest.Hooks{
		Login: func(metas map[string]string) (bool, string) {
			logins <- metas["box_id"]
			return true, ""
		},
		Subdomain: func(metas map[string]string) string { return metas["box_id"] },
	})

	run := func(caFile, boxID string) {
		svr, err := relay.NewFrpClient(relay.FrpConfig{
			Server:     r.BoxAddr,
			CAFile:     caFile,
			ServerName: "relay." + r.BoxDomain,
			Metas:      map[string]string{"box_id": boxID},
			ProxyName:  boxID,
			LocalAddr:  "127.0.0.1:1",
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = svr.Run(ctx) }()
		t.Cleanup(func() { cancel(); <-done })
	}

	// A relay whose certificate the CA file doesn't vouch for never sees the box's metas.
	run(otherCA(t), "fooled")
	// The same relay, trusted: the box logs in.
	run(r.CAFile, "trusting")

	// The trusting box logs in; the fooled one, started first, has had its tries by then and
	// two seconds more.
	deadline := time.After(10 * time.Second)
	for trusted := false; !trusted; {
		select {
		case id := <-logins:
			if id == "fooled" {
				t.Fatal("the box logged in to a relay its CA file doesn't trust")
			}
			trusted = true
		case <-deadline:
			t.Fatal("the box trusting the relay never logged in")
		}
	}
	grace := time.After(2 * time.Second)
	for {
		select {
		case id := <-logins:
			if id == "fooled" {
				t.Fatal("the box logged in to a relay its CA file doesn't trust")
			}
		case <-grace:
			return
		}
	}
}

// otherCA writes a CA of its own, one that signed nothing the relay holds.
func otherCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "another CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "other-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
