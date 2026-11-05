package signins

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// screenOpen is whether the site's screen is the owner's to see: a run holds the site and either the
// run is in flight on it, whether or not it waits for them. The screen is look-only outside a wait:
// the worker alone switches the box's input, and only during one.
func (s *Service) screenOpen(site string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current[site] != 0
}

type sockKey struct{}

// screenDialTimeout bounds reaching websockify's socket. Its answer to the upgrade is bounded by the
// transport's ResponseHeaderTimeout; the websocket it opens has no deadline, it lasts as long as the
// owner has the page open.
const screenDialTimeout = 5 * time.Second

// screenTransport dials the unix socket the request's context names; one for all requests.
// No keep-alive: every site's request goes to the same host name "screen", so a pooled connection
// could carry one site's request to another's socket.
var screenTransport = &http.Transport{
	DisableKeepAlives:     true,
	ResponseHeaderTimeout: 10 * time.Second,
	DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: screenDialTimeout}).DialContext(ctx, "unix", ctx.Value(sockKey{}).(string))
	},
}

// screenProxy is the one proxy for every site's screen; the site's socket rides on the context.
var screenProxy = &httputil.ReverseProxy{
	Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "screen"
		pr.Out.URL.Path, pr.Out.URL.RawPath = "/websockify", ""
		pr.Out.Host = "screen"
		pr.Out.Header.Del("Origin")        // websockify checks none; the engine did
		pr.Out.Header.Del("Authorization") // the owner's console login stays here
	},
	Transport: screenTransport,
	ErrorLog:  log.New(io.Discard, "", 0),
	// The handler set frame-ancestors on the response already; websockify's own policy is not added.
	ModifyResponse: func(resp *http.Response) error {
		resp.Header.Del("Content-Security-Policy")
		return nil
	},
	ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "the screen is not there", http.StatusBadGateway)
	},
}

// frameAncestors: only the console may frame the screen, or another page could clickjack the owner.
const frameAncestors = "frame-ancestors 'self'"

// screenSocket is the one path under …/screen/ that reaches the run: noVNC's websocket.
const screenSocket = "websockify"

// screen is the site's run screen in the console. noVNC's own files come from the box's package
// (NoVNCDir), whether or not a run is up: they load at once and the console's view keeps trying the
// socket until the run's is up, through the restart between a flow and its check too. Only the websocket reaches the
// run, proxied to the worker's websockify socket, and only while the screen is open (screenOpen).
// Behind the console login like every route; nothing is served without a screens dir. Nothing
// proxied is logged: the websocket carries the owner's keys and clicks.
func (h *handler) screen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", frameAncestors)
	site := r.PathValue("site")
	if h.svc.cfg.ScreensDir == "" || !isKey(site) {
		http.NotFound(w, r)
		return
	}
	// The browser attaches the console's cached Basic credentials to any page's websocket, so a
	// page on another origin could drive the screen: any upgrade must come from the console itself.
	if r.Header.Get("Upgrade") != "" {
		o, err := url.Parse(r.Header.Get("Origin"))
		if err != nil || o.Host == "" || o.Host != r.Host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	if r.PathValue("path") != screenSocket {
		files := r.Clone(r.Context())
		files.URL.Path, files.URL.RawPath = "/"+r.PathValue("path"), ""
		http.FileServerFS(os.DirFS(h.svc.cfg.NoVNCDir)).ServeHTTP(w, files)
		return
	}
	if !h.svc.screenOpen(site) {
		http.NotFound(w, r)
		return
	}
	sock := filepath.Join(h.svc.cfg.ScreensDir, site, "web.sock")
	screenProxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sockKey{}, sock)))
}
