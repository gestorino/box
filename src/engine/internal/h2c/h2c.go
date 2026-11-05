// Package h2c is how the box's services talk over their Unix sockets — the engine to hostd and the
// connector daemons (email, SMS, voice, WhatsApp, Telegram), the daemons to hostd's settings socket:
// unencrypted HTTP/2 only, at both ends.
package h2c

import (
	"context"
	"net"
	"net/http"
)

// Protocols is every service's http.Server's: unencrypted HTTP/2 and nothing else.
func Protocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return p
}

// Transport speaks unencrypted HTTP/2 to the service listening on socket; the host in a request's
// URL is only a label.
func Transport(socket string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		Protocols: Protocols(),
	}
}
