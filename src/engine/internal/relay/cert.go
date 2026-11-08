package relay

import (
	"context"
	"crypto/tls"
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
// in its file storage under dir, and renews it.
type Cert struct {
	dir   string
	cache *certmagic.Cache
	cfg   *certmagic.Config
	acme  *certmagic.ACMEIssuer

	mu   sync.Mutex
	name string // the managed name, "" when none
}

// NewCert is the front's certificate, its storage in dir and email the ACME account's contact.
// front is the front's host:port: certmagic's challenge "server" is pointed at it, so certmagic
// finds the port held and leaves the answering to the front (it binds nothing of its own, and
// never port 443, which relayd may not bind). The front must be listening before Manage.
func NewCert(dir, email, front string) (*Cert, error) {
	host, p, err := net.SplitHostPort(front)
	if err != nil {
		return nil, fmt.Errorf("relay: front %q: %w", front, err)
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return nil, fmt.Errorf("relay: front %q: %w", front, err)
	}
	logger := zap.New(slogCore{})
	c := &Cert{dir: dir}
	c.cache = certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return c.cfg, nil },
		Logger:           logger,
	})
	c.cfg = certmagic.New(c.cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: dir},
		Logger:  logger,
	})
	c.acme = certmagic.NewACMEIssuer(c.cfg, certmagic.ACMEIssuer{
		CA:                      certmagic.LetsEncryptProductionCA,
		Email:                   email,
		Agreed:                  true,
		DisableHTTPChallenge:    true,
		DisableTLSALPNChallenge: false,
		ListenHost:              host,
		AltTLSALPNPort:          port,
		Logger:                  logger,
	})
	c.cfg.Issuers = []certmagic.Issuer{c.acme}
	return c, nil
}

// Manage gets the certificate for name in the background (or loads it from storage) and keeps
// it renewed; obtaining stops when ctx ends. Managing another name stops keeping the old one.
func (c *Cert) Manage(ctx context.Context, name string) error {
	c.mu.Lock()
	old := c.name
	c.name = name
	c.mu.Unlock()
	if old != "" && old != name {
		c.cache.RemoveManaged([]certmagic.SubjectIssuer{{Subject: old}})
	}
	return c.cfg.ManageAsync(ctx, []string{name})
}

// ServerCert is the front's certificate for a hello, when certmagic holds one for its name.
func (c *Cert) ServerCert(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	return c.cfg.GetCertificate(hello)
}

// ALPNChallenge answers a TLS-ALPN-01 hello (the front sends only those here) with the
// challenge certificate certmagic made for the name it is validating.
func (c *Cert) ALPNChallenge(*tls.ClientHelloInfo) (*tls.Config, bool) {
	return c.cfg.TLSConfig(), true
}

// Ready says a certificate for the managed name is held and not expired.
func (c *Cert) Ready() bool {
	c.mu.Lock()
	name := c.name
	c.mu.Unlock()
	if name == "" {
		return false
	}
	for _, cert := range c.cache.AllMatchingCertificates(name) {
		if cert.Leaf != nil && time.Now().Before(cert.Leaf.NotAfter) {
			return true
		}
	}
	return false
}

// Forget stops keeping the certificate and wipes certmagic's storage: the certificate, its key
// and the ACME account (dir holds only certmagic's). Call it with the tunnel stopped, so no
// obtain is under way. Idempotent.
func (c *Cert) Forget() error {
	c.mu.Lock()
	name := c.name
	c.name = ""
	c.mu.Unlock()
	if name != "" {
		c.cache.RemoveManaged([]certmagic.SubjectIssuer{{Subject: name}})
	}
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
