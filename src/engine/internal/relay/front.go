package relay

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"sync"
	"time"
)

const viaHeader = "X-Concierge-Via"

const acmeALPN = "acme-tls/1"

// Front is relayd's TLS server: ServeTLS-like Serve, Shutdown, Close, and CloseDevice.
type Front struct {
	srv    *http.Server
	active func(serial string) bool

	mu    sync.Mutex
	conns map[string]map[*deviceConn]struct{} // by device serial
}

// deviceConn is the raw TCP connection under a device's TLS one; closing it unregisters it.
type deviceConn struct {
	net.Conn
	f      *Front
	serial string // set once, under f.mu
}

func (c *deviceConn) Close() error {
	c.f.forget(c)
	return c.Conn.Close()
}

type trackListener struct {
	net.Listener
	f *Front
}

func (l trackListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &deviceConn{Conn: c, f: l.f}, nil
}

type connKey struct{}

func (f *Front) remember(c *deviceConn, serial string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.serial != "" {
		return
	}
	c.serial = serial
	if f.conns[serial] == nil {
		f.conns[serial] = map[*deviceConn]struct{}{}
	}
	f.conns[serial][c] = struct{}{}
}

func (f *Front) forget(c *deviceConn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.conns[c.serial]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(f.conns, c.serial)
		}
	}
}

// CloseDevice closes every open connection of the device with this serial, upgraded
// WebSockets included.
func (f *Front) CloseDevice(serial string) {
	f.mu.Lock()
	var open []*deviceConn
	for c := range f.conns[serial] {
		open = append(open, c)
	}
	f.mu.Unlock()
	for _, c := range open {
		c.Close()
	}
}

// Serve serves TLS on ln until Close or Shutdown.
func (f *Front) Serve(ln net.Listener) error {
	return f.srv.Serve(tls.NewListener(trackListener{ln, f}, f.srv.TLSConfig))
}

func (f *Front) Shutdown(ctx context.Context) error { return f.srv.Shutdown(ctx) }
func (f *Front) Close() error                       { return f.srv.Close() }

// NewFront is relayd's TLS server. A handshake completes only for a client certificate the CA
// issued and active still says yes to (checked on every handshake, and on every request; tickets
// are off so no session resumes around it). A hello offering exactly acme-tls/1 is handed to
// alpnChallenge instead, with no client certificate asked and no other protocol possible. What
// passes is proxied to engine (host:port) marked X-Concierge-Via: relay.
func NewFront(ca *CA, active func(serial string) bool,
	serverCert func(*tls.ClientHelloInfo) (*tls.Certificate, error),
	alpnChallenge func(*tls.ClientHelloInfo) (*tls.Config, bool), engine string) *Front {
	f := &Front{active: active, conns: map[string]map[*deviceConn]struct{}{}}
	clients := &tls.Config{
		MinVersion:             tls.VersionTLS12,
		ClientAuth:             tls.RequireAndVerifyClientCert,
		ClientCAs:              ca.Pool(),
		GetCertificate:         serverCert,
		NextProtos:             []string{"http/1.1"},
		SessionTicketsDisabled: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !active(cs.PeerCertificates[0].SerialNumber.Text(16)) {
				return errNotActive
			}
			return nil
		},
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: engine})
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del(viaHeader)
			pr.Out.Header.Set(viaHeader, "relay")
		},
	}
	f.srv = &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
		// No HTTP/2: a live view's WebSocket needs HTTP/1.1.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if tc, ok := c.(*tls.Conn); ok {
				if dc, ok := tc.NetConn().(*deviceConn); ok {
					return context.WithValue(ctx, connKey{}, dc)
				}
			}
			return ctx
		},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			serial := r.TLS.PeerCertificates[0].SerialNumber.Text(16)
			if !f.active(serial) {
				w.Header().Set("Connection", "close")
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if dc, ok := r.Context().Value(connKey{}).(*deviceConn); ok {
				f.remember(dc, serial)
			}
			proxy.ServeHTTP(w, r)
		}),
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
				// RFC 8737 §6: the challenge is a hello offering acme-tls/1 alone.
				if slices.Equal(h.SupportedProtos, []string{acmeALPN}) {
					if c, ok := alpnChallenge(h); ok {
						c = c.Clone()
						c.NextProtos = []string{acmeALPN}
						return c, nil
					}
				}
				return clients, nil
			},
		},
	}
	return f
}

type notActive struct{}

func (notActive) Error() string { return "device not active" }

var errNotActive error = notActive{}
