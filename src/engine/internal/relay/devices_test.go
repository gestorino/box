package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"testing"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"

	"concierge/engine/internal/secrets/secretstest"
)

func TestIssueMakesAPhoneCertificate(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ca, err := LoadCA(context.Background(), secretstest.Start(t, "relay"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	serial, p12, password, expires, err := ca.Issue("Nik's iPhone")
	if err != nil {
		t.Fatal(err)
	}
	if len(password) < 16 {
		t.Fatalf("password too short: %d", len(password))
	}
	key, leaf, chain, err := pkcs12.DecodeChain(p12, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := key.(*ecdsa.PrivateKey); !ok {
		t.Fatalf("key is %T", key)
	}
	if len(chain) != 1 || !chain[0].IsCA {
		t.Fatalf("chain should carry the CA, got %d", len(chain))
	}
	if leaf.Subject.CommonName != "Nik's iPhone" || leaf.SerialNumber.Text(16) != serial {
		t.Fatalf("leaf %q serial %s vs %s", leaf.Subject.CommonName, leaf.SerialNumber.Text(16), serial)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: ca.Pool(), CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("leaf does not chain: %v", err)
	}
	if want := now.AddDate(1, 0, 0); !expires.Equal(want) || !leaf.NotAfter.Equal(want) {
		t.Fatalf("expires %v, leaf %v, want %v", expires, leaf.NotAfter, want)
	}
	if !leaf.NotBefore.Before(now) {
		t.Fatal("NotBefore should be a little before now")
	}
	if _, _, _, err := pkcs12.DecodeChain(p12, "wrong"); err == nil {
		t.Fatal("wrong password decoded")
	}
}

func TestIssuedSerialsDiffer(t *testing.T) {
	ca, err := LoadCA(context.Background(), secretstest.Start(t, "relay"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	a, _, pa, _, _ := ca.Issue("a")
	b, _, pb, _, _ := ca.Issue("b")
	if a == b || pa == pb {
		t.Fatal("serials or passwords repeat")
	}
}

func TestLoadCATwiceIsTheSameCA(t *testing.T) {
	sec := secretstest.Start(t, "relay")
	a, err := LoadCA(context.Background(), sec, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadCA(context.Background(), sec, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if !a.cert.Equal(b.cert) {
		t.Fatal("a second load made a new CA")
	}
	_, p12, pw, _, _ := a.Issue("x")
	_, leaf, _, _ := pkcs12.DecodeChain(p12, pw)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: b.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCAWithSecretdDownFails(t *testing.T) {
	if _, err := LoadCA(context.Background(), secretstest.Down(t), time.Now); err == nil {
		t.Fatal("expected an error")
	}
}
