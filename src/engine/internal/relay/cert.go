package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var _ CertManager = (*Cert)(nil)

// Cert is the front's certificate for <name>.<box-domain>, from Let's Encrypt by TLS-ALPN-01
// only (port 80 doesn't reach the box): the CA's check arrives through the relay at the front,
// which hands it to ALPNChallenge. certmagic keeps the account key, the certificate and its key
// in its file storage in dir (<state>/certmagic, its alone), and renews it.
//
// certmagic's ACME contact is fixed when it is built, so it is built at the first Manage, with
// that claim's email, built again (the old cache stopped) only when the email changes, and
// dropped by Forget: a box claimed again starts from nothing.
type Cert struct {
	dir   string
	email string // a fixed ACME contact; "" is the account's, as Manage is given it
	host  string
	port  int

	mu      sync.Mutex
	contact string // the email the current certmagic was built with
	cache   *certmagic.Cache
	cfg     *certmagic.Config
	acme    *certmagic.ACMEIssuer
	name    string // the managed name, "" when none
}

// certmagicDir is certmagic's own directory under relayd's state directory: Forget empties it,
// and nothing else lives in it.
const certmagicDir = "certmagic"

var errNoCert = errors.New("relay: no certificate yet")

// NewCert is the front's certificate, its storage in <stateDir>/certmagic (made 0700; stateDir
// must be absolute, or certmagic would write keys relative to the working directory). email is a
// fixed ACME contact, or "" for the account's email each Manage is given. front is the front's
// host:port: certmagic's challenge "server" is pointed at it, so certmagic finds the port held
// and leaves the answering to the front (it binds nothing of its own, and never port 443, which
// relayd may not bind). The front must be listening before Manage.
func NewCert(stateDir, email, front string) (*Cert, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) {
		return nil, fmt.Errorf("relay: the state directory %q must be absolute", stateDir)
	}
	dir := filepath.Join(stateDir, certmagicDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("relay: certmagic's directory: %w", err)
	}
	host, p, err := net.SplitHostPort(front)
	if err != nil {
		return nil, fmt.Errorf("relay: front %q: %w", front, err)
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return nil, fmt.Errorf("relay: front %q: %w", front, err)
	}
	return &Cert{dir: dir, email: email, host: host, port: port}, nil
}

// use makes certmagic's config for the account's email (or the fixed one), unless the current
// one already has that contact, and returns it.
func (c *Cert) use(account string) *certmagic.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	contact := c.email
	if contact == "" {
		contact = account
	}
	if c.cfg != nil && c.contact == contact {
		return c.cfg
	}
	c.drop()
	logger := zap.New(slogCore{})
	var cfg *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return cfg, nil },
		Logger:           logger,
	})
	cfg = certmagic.New(cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: c.dir},
		Logger:  logger,
	})
	acme := certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{
		CA:                      certmagic.LetsEncryptProductionCA,
		Email:                   contact,
		Agreed:                  true,
		DisableHTTPChallenge:    true,
		DisableTLSALPNChallenge: false,
		ListenHost:              c.host,
		AltTLSALPNPort:          c.port,
		Logger:                  logger,
	})
	cfg.Issuers = []certmagic.Issuer{acme}
	c.contact, c.cache, c.cfg, c.acme = contact, cache, cfg, acme
	return cfg
}

// drop stops the current certmagic, if any: its cache stops renewing. Call with c.mu held.
func (c *Cert) drop() {
	if c.cache != nil {
		c.cache.Stop()
	}
	c.contact, c.cache, c.cfg, c.acme, c.name = "", nil, nil, nil, ""
}

// Manage gets the certificate for name in the background (or loads it from storage) and keeps
// it renewed, its ACME contact the account's email (unless a fixed one was given); obtaining
// stops when ctx ends. Managing another name stops keeping the old one.
func (c *Cert) Manage(ctx context.Context, email, name string) error {
	cfg := c.use(email) // a new certmagic manages nothing yet: drop cleared the name
	c.mu.Lock()
	if c.name != "" && c.name != name {
		c.cache.RemoveManaged([]certmagic.SubjectIssuer{{Subject: c.name}})
	}
	c.name = name
	c.mu.Unlock()
	return cfg.ManageAsync(ctx, []string{name})
}

func (c *Cert) config() *certmagic.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// ServerCert is the front's certificate for a hello, when certmagic holds one for its name.
func (c *Cert) ServerCert(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cfg := c.config()
	if cfg == nil {
		return nil, errNoCert
	}
	return cfg.GetCertificate(hello)
}

// ALPNChallenge answers a TLS-ALPN-01 hello (the front sends only those here) with the
// challenge certificate certmagic made for the name it is validating; none before a Manage.
func (c *Cert) ALPNChallenge(*tls.ClientHelloInfo) (*tls.Config, bool) {
	cfg := c.config()
	if cfg == nil {
		return nil, false
	}
	return cfg.TLSConfig(), true
}

// Ready says a certificate for the managed name is held and not expired.
func (c *Cert) Ready() bool {
	c.mu.Lock()
	name, cache := c.name, c.cache
	c.mu.Unlock()
	if name == "" {
		return false
	}
	for _, cert := range cache.AllMatchingCertificates(name) {
		if cert.Leaf != nil && time.Now().Before(cert.Leaf.NotAfter) {
			return true
		}
	}
	return false
}

// Forget stops certmagic and empties its own directory: the certificate, its key and the ACME
// account; the state directory around it is left alone. Call it with the tunnel stopped
// (FrpTunnel.Forget stops it), so no obtain is under way. Idempotent.
func (c *Cert) Forget() error {
	c.mu.Lock()
	c.drop()
	c.mu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("relay: forget the certificate: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(c.dir, e.Name())); err != nil {
			return fmt.Errorf("relay: forget the certificate: %w", err)
		}
	}
	return nil
}

// slogCore is a zap core writing certmagic's records to slog.Default from info up. certmagic
// logs names, issuers and the CA's errors; never a key.
type slogCore struct{ attrs []slog.Attr }

func (c slogCore) Enabled(l zapcore.Level) bool { return l >= zapcore.InfoLevel }

func (c slogCore) With(fields []zapcore.Field) zapcore.Core {
	return slogCore{attrs: append(append([]slog.Attr(nil), c.attrs...), zapAttrs(fields)...)}
}

func (c slogCore) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(e.Level) {
		return ce.AddCore(e, c)
	}
	return ce
}

func (c slogCore) Write(e zapcore.Entry, fields []zapcore.Field) error {
	lv := slog.LevelInfo
	switch {
	case e.Level >= zapcore.ErrorLevel:
		lv = slog.LevelError
	case e.Level == zapcore.WarnLevel:
		lv = slog.LevelWarn
	}
	attrs := append(append([]slog.Attr(nil), c.attrs...), zapAttrs(fields)...)
	if e.LoggerName != "" {
		attrs = append(attrs, slog.String("logger", e.LoggerName))
	}
	slog.LogAttrs(context.Background(), lv, "certmagic: "+e.Message, attrs...)
	return nil
}

func (slogCore) Sync() error { return nil }

func zapAttrs(fields []zapcore.Field) []slog.Attr {
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range fields {
		f.AddTo(enc)
	}
	attrs := make([]slog.Attr, 0, len(enc.Fields))
	for k, v := range enc.Fields {
		attrs = append(attrs, slog.Any(k, v))
	}
	return attrs
}
