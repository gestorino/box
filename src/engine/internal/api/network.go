package api

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"concierge/engine/internal/h2c"
)

// SocketProxy forwards requests to a local service (hostd, a connector daemon) over its Unix socket,
// in unencrypted HTTP/2, with /api/v1 stripped. The service is the authority on its routes and
// input; the engine adds only the login and the write guard in front (New). name is how 503s refer
// to it; an empty socket means the service is not configured.
func SocketProxy(socket, name string) http.Handler {
	if socket == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			unavailable(w, name+" not configured")
		})
	}
	return &httputil.ReverseProxy{
		Transport: h2c.Transport(socket),
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(&url.URL{Scheme: "http", Host: "hostd"})
			// As written: a decoded %2F would become a path separator the service routes on.
			r.Out.URL.Path = strings.TrimPrefix(r.In.URL.Path, "/api/v1")
			r.Out.URL.RawPath = strings.TrimPrefix(r.In.URL.EscapedPath(), "/api/v1")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Warn(name+" unreachable", "err", err)
			unavailable(w, name+" unreachable")
		},
	}
}

func unavailable(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	writeJSON(w, map[string]string{"error": msg})
}
