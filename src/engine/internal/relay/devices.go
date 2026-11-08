package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"

	"concierge/engine/internal/secrets"
)

const (
	caKeyName  = "device-ca-key"  // PKCS#8 DER, base64
	caCertName = "device-ca-cert" // DER, base64 (public, kept beside the key so a load is the same CA)

	// passwordAlphabet is typed by hand on a phone: no 0/O, 1/l/I look-alikes.
	passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	passwordLength   = 20
)

// CA is the box's own device CA: it issues the client certificates phones present to relayd.
type CA struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	now  func() time.Time
}

// LoadCA reads the CA from secretd, making it (P-256, ten years) on first use.
func LoadCA(ctx context.Context, sec *secrets.Client, now func() time.Time) (*CA, error) {
	keyB64, keyOK, err := sec.Get(ctx, caKeyName)
	if err != nil {
		return nil, err
	}
	certB64, certOK, err := sec.Get(ctx, caCertName)
	if err != nil {
		return nil, err
	}
	if keyOK && certOK {
		return parseCA(keyB64, certB64, now)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	t := now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Concierge box device CA"},
		NotBefore:             t.Add(-5 * time.Minute),
		NotAfter:              t.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	// The cert first: a key without its cert is retried as a fresh pair, a cert without its key never is.
	if err := sec.Put(ctx, caCertName, base64.StdEncoding.EncodeToString(der)); err != nil {
		return nil, err
	}
	if err := sec.Put(ctx, caKeyName, base64.StdEncoding.EncodeToString(keyDER)); err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{key: key, cert: cert, now: now}, nil
}

func parseCA(keyB64, certB64 string, now func() time.Time) (*CA, error) {
	keyDER, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("device CA key: %w", err)
	}
	k, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, fmt.Errorf("device CA key: %w", err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("device CA key: not an ECDSA key")
	}
	certDER, err := base64.StdEncoding.DecodeString(certB64)
	if err != nil {
		return nil, fmt.Errorf("device CA cert: %w", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("device CA cert: %w", err)
	}
	return &CA{key: key, cert: cert, now: now}, nil
}

// Pool is the CA alone, for relayd's client verification.
func (c *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)
	return p
}

// Issue makes a one-year client certificate named label and returns it as a password-protected
// .p12 with the CA in its chain. serial is the leaf's serial in hex. The leaf key lives only here.
func (c *CA) Issue(label string) (serial string, p12 []byte, password string, expires time.Time, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", nil, "", time.Time{}, err
	}
	sn, err := newSerial()
	if err != nil {
		return "", nil, "", time.Time{}, err
	}
	t := c.now()
	expires = t.AddDate(1, 0, 0)
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: label},
		NotBefore:             t.Add(-5 * time.Minute),
		NotAfter:              expires,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return "", nil, "", time.Time{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return "", nil, "", time.Time{}, err
	}
	password, err = newPassword()
	if err != nil {
		return "", nil, "", time.Time{}, err
	}
	// LegacyRC2 (3DES keys, RC2 certs, SHA-1 MAC): the encoding Apple's and Android's importers accept.
	p12, err = pkcs12.LegacyRC2.Encode(key, leaf, []*x509.Certificate{c.cert}, password)
	if err != nil {
		return "", nil, "", time.Time{}, err
	}
	return sn.Text(16), p12, password, expires, nil
}

func newSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func newPassword() (string, error) {
	b := make([]byte, passwordLength)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = passwordAlphabet[n.Int64()]
	}
	return string(b), nil
}
