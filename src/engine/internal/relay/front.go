package relay

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"time"
)

const viaHeader = "X-Concierge-Via"

const acmeALPN = "acme-tls/1"

// NewFront is relayd's TLS server. A handshake completes only for a client certificate the CA
// issued and active still says yes to (checked on every handshake, resumed ones included: tickets
// are off too). A hello offering acme-tls/1 is handed to alpnChallenge instead, with no client
// certificate asked. What passes is proxied to engine (host:port) marked X-Concierge-Via: relay.
func NewFront(ca *CA, active func(serial string) bool,
	serverCert func(*tls.ClientHelloInfo) (*tls.Certificate, error),
	alpnChallenge func(*tls.ClientHelloInfo) (*tls.Config, bool), engine string) *http.Server {
	clients := &tls.Config{
		MinVersion:             tls.VersionTLS12,
		ClientAuth:             tls.RequireAndVerifyClientCert,
		ClientCAs:              ca.Pool(),
		GetCertificate:         serverCert,
		NextProtos:             []string{"http/1.1"},
		SessionTicketsDisabled: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !active(cs.PeerCertificates[0].SerialNumber.Text(16)) {
				return errors.New("device not active")
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
	return &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		// No HTTP/2: a live view's WebSocket needs HTTP/1.1.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
				if slices.Contains(h.SupportedProtos, acmeALPN) {
					if c, ok := alpnChallenge(h); ok {
						return c, nil
					}
				}
				return clients, nil
			},
		},
	}
}
