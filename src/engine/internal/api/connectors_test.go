package api

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"concierge/engine/internal/channels"
	"concierge/engine/internal/h2c"
)

// answering is a service on a Unix socket that answers "METHOD /path" from its table (404 for
// anything else) and records every request it saw.
type answering struct {
	mu   sync.Mutex
	seen []string
	tbl  map[string]struct {
		status int
		body   string
	}
}

func (a *answering) set(key string, status int, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tbl[key] = struct {
		status int
		body   string
	}{status, body}
}

func (a *answering) got() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seen...)
}

// serveAnswering serves as hostd and the connectors do, unencrypted HTTP/2 only.
func serveAnswering(t *testing.T) (*answering, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cf") // short: macOS caps socket paths at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	a := &answering{tbl: map[string]struct {
		status int
		body   string
	}{}}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		key := r.Method + " " + r.URL.EscapedPath() // as it came on the wire
		a.mu.Lock()
		a.seen = append(a.seen, key)
		ans, ok := a.tbl[key]
		a.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if ans.status == http.StatusNoContent {
			w.WriteHeader(ans.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(ans.status)
		w.Write([]byte(ans.body))
	}), Protocols: h2c.Protocols()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return a, socket
}

type confirmRig struct {
	h                                http.Handler
	hostd, sms, calls, email, wa, tg *answering
}

func newConfirmRig(t *testing.T) confirmRig {
	t.Helper()
	var g confirmRig
	var hs, ss, cs, es, ws, ts string
	g.hostd, hs = serveAnswering(t)
	g.sms, ss = serveAnswering(t)
	g.calls, cs = serveAnswering(t)
	g.email, es = serveAnswering(t)
	g.wa, ws = serveAnswering(t)
	g.tg, ts = serveAnswering(t)
	reg, _ := channels.NewRegistry()
	g.h = New(Config{Login: testLogin, Channels: reg, Network: SocketProxy(hs, "host service"),
		SMS: SocketProxy(ss, "SMS service"), Calls: SocketProxy(cs, "call service"), Email: SocketProxy(es, "email service"),
		WhatsApp: SocketProxy(ws, "WhatsApp service"), Telegram: SocketProxy(ts, "Telegram service")})
	return g
}

func TestConfirmAsksTheDaemonThenHostd(t *testing.T) {
	g := newConfirmRig(t)
	for _, c := range []struct {
		name string
		svc  *answering
		path string
	}{{"sms", g.sms, "/sms/check"}, {"voice", g.calls, "/calls/check"}, {"email", g.email, "/email/check"},
		{"whatsapp", g.wa, "/whatsapp/check"}, {"telegram", g.tg, "/telegram/check"}} {
		c.svc.set("GET "+c.path, http.StatusOK, `{"state":"passed"}`)
		g.hostd.set("POST /connectors/"+c.name+"/verified", http.StatusNoContent, "")
		if rec := send(g.h, "POST", "/api/v1/connectors/"+c.name+"/confirm", "", sameOriginJSON); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		if got := c.svc.got(); strings.Join(got, "|") != "GET "+c.path {
			t.Fatalf("%s: daemon saw %q", c.name, got)
		}
	}
	want := "POST /connectors/sms/verified|POST /connectors/voice/verified|POST /connectors/email/verified|POST /connectors/whatsapp/verified|POST /connectors/telegram/verified"
	if got := strings.Join(g.hostd.got(), "|"); got != want {
		t.Fatalf("hostd saw %q", got)
	}
}

func TestConfirmRefusesUnverified(t *testing.T) {
	g := newConfirmRig(t)
	for _, state := range []string{"waiting", "none"} {
		g.sms.set("GET /sms/check", http.StatusOK, `{"state":"`+state+`"}`)
		rec := send(g.h, "POST", "/api/v1/connectors/sms/confirm", "", sameOriginJSON)
		if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"not verified yet"}` {
			t.Fatalf("%s: %d %s", state, rec.Code, rec.Body)
		}
	}
	if len(g.hostd.got()) != 0 {
		t.Fatalf("hostd saw %q", g.hostd.got())
	}
}

func TestConfirmPassesHostdsRefusal(t *testing.T) {
	g := newConfirmRig(t)
	g.email.set("GET /email/check", http.StatusOK, `{"state":"passed"}`)
	g.hostd.set("POST /connectors/email/verified", http.StatusConflict, `{"error":"not being set up"}`)
	rec := send(g.h, "POST", "/api/v1/connectors/email/confirm", "", sameOriginJSON)
	if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"not being set up"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestConfirmPassesTheDaemonsError(t *testing.T) {
	g := newConfirmRig(t)
	rec := send(g.h, "POST", "/api/v1/connectors/sms/confirm", "", sameOriginJSON) // smsd answers 404
	if rec.Code != http.StatusNotFound {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	reg, _ := channels.NewRegistry()
	hostd, hs := serveAnswering(t)
	h := New(Config{Login: testLogin, Channels: reg, Network: SocketProxy(hs, "host service"), SMS: SocketProxy("", "SMS service")})
	rec = send(h, "POST", "/api/v1/connectors/sms/confirm", "", sameOriginJSON)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "SMS service not configured") {
		t.Fatalf("unconfigured: %d %s", rec.Code, rec.Body)
	}
	if len(hostd.got()) != 0 {
		t.Fatalf("hostd saw %q", hostd.got())
	}
}

func TestConfirmUnknownName(t *testing.T) {
	g := newConfirmRig(t)
	if rec := send(g.h, "POST", "/api/v1/connectors/fax/confirm", "", sameOriginJSON); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
}

func TestConfirmIsAGuardedWrite(t *testing.T) {
	g := newConfirmRig(t)
	g.sms.set("GET /sms/check", http.StatusOK, `{"state":"passed"}`)
	cross := map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}
	if rec := send(g.h, "POST", "/api/v1/connectors/sms/confirm", "", cross); rec.Code != http.StatusForbidden {
		t.Fatalf("%d", rec.Code)
	}
}

func TestConsoleCannotReachVerified(t *testing.T) {
	g := newConfirmRig(t)
	g.hostd.set("POST /connectors/sms/verified", http.StatusNoContent, "")
	// An encoded slash must not smuggle "/verified" through the add forward's {name}.
	for _, path := range []string{"/api/v1/connectors/sms/verified", "/api/v1/connectors/sms%2Fverified", "/api/v1/connectors/sms%2fverified"} {
		for _, method := range []string{"POST", "DELETE"} {
			if rec := send(g.h, method, path, "", sameOriginJSON); rec.Code < 400 {
				t.Fatalf("%s %s: %d", method, path, rec.Code)
			}
		}
	}
	if len(g.hostd.got()) != 0 {
		t.Fatalf("hostd saw %q", g.hostd.got())
	}
}

func TestAddAndRemoveForwardOnlyTheFiveConnectors(t *testing.T) {
	g := newConfirmRig(t)
	g.hostd.set("POST /connectors/sms", http.StatusNoContent, "")
	g.hostd.set("DELETE /connectors/sms", http.StatusNoContent, "")
	send(g.h, "POST", "/api/v1/connectors/sms", "", sameOriginJSON)
	send(g.h, "DELETE", "/api/v1/connectors/sms", "", sameOriginJSON)
	if rec := send(g.h, "POST", "/api/v1/connectors/fax", "", sameOriginJSON); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}
	if got := strings.Join(g.hostd.got(), "|"); got != "POST /connectors/sms|DELETE /connectors/sms" {
		t.Fatalf("hostd saw %q", got)
	}
}

// The proxy forwards a path as it was written: an encoded character stays encoded.
func TestProxyKeepsThePathEncoded(t *testing.T) {
	g := newConfirmRig(t)
	send(g.h, "GET", "/api/v1/email/emails/a%2Fb/html", "", nil)
	if got := g.email.got(); len(got) != 1 || got[0] != "GET /email/emails/a%2Fb/html" {
		t.Fatalf("%q", got)
	}
}

func TestCheckRoutesAreForwarded(t *testing.T) {
	g := newConfirmRig(t)
	for _, k := range []string{"POST /sms/check", "GET /calls/check", "POST /calls/check", "POST /email/check", "POST /whatsapp/check", "POST /telegram/check"} {
		method, path, _ := strings.Cut(k, " ")
		send(g.h, method, "/api/v1"+path, "{}", sameOriginJSON)
	}
	if got := strings.Join(g.sms.got(), "|"); got != "POST /sms/check" {
		t.Fatalf("smsd saw %q", got)
	}
	if got := strings.Join(g.calls.got(), "|"); got != "GET /calls/check|POST /calls/check" {
		t.Fatalf("calld saw %q", got)
	}
	for name, a := range map[string]*answering{"email": g.email, "whatsapp": g.wa, "telegram": g.tg} {
		if got := a.got(); len(got) != 1 {
			t.Fatalf("%s saw %q", name, got)
		}
	}
}

// The owner's OpenAI key goes to calld, its only keeper; the one-way check's answer is gone.
func TestCallsKeyRoutesAreForwarded(t *testing.T) {
	g := newConfirmRig(t)
	send(g.h, "GET", "/api/v1/calls/key", "", nil)
	send(g.h, "PUT", "/api/v1/calls/key", `{"key":"sk-x"}`, sameOriginJSON)
	send(g.h, "POST", "/api/v1/calls/check/answer", `{"code":"472"}`, sameOriginJSON)
	if got := strings.Join(g.calls.got(), "|"); got != "GET /calls/key|PUT /calls/key" {
		t.Fatalf("calld saw %q", got)
	}
}
