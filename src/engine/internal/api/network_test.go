package api

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"concierge/engine/internal/channels"
	"concierge/engine/internal/h2c"
)

// fakeDaemon serves on a Unix socket as hostd and the connectors do, unencrypted HTTP/2 only, and
// records what reached it.
func fakeDaemon(t *testing.T) (socket string, seen *[]string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "hd") // short: macOS caps socket paths at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket = filepath.Join(dir, "h.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"from":"hostd"}`))
	}), Protocols: h2c.Protocols()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return socket, &got
}

func send(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "192.0.2.10"
	r.SetBasicAuth("helper", "helper")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func engineWith(t *testing.T, network http.Handler) http.Handler {
	t.Helper()
	reg, err := channels.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Login: testLogin, Channels: reg, Network: network})
}

var sameOriginJSON = map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}

func TestProxyForwardsWithoutPrefix(t *testing.T) {
	socket, seen := fakeDaemon(t)
	h := engineWith(t, SocketProxy(socket, "host service"))

	rec := send(h, "GET", "/api/v1/network/wifi", "", nil)
	if rec.Code != http.StatusAccepted || rec.Body.String() != `{"from":"hostd"}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	send(h, "POST", "/api/v1/network/wifi/connect", `{"id":"x"}`, sameOriginJSON)
	want := []string{"GET /network/wifi ", `POST /network/wifi/connect {"id":"x"}`}
	if len(*seen) != 2 || (*seen)[0] != want[0] || (*seen)[1] != want[1] {
		t.Fatalf("hostd saw %q, want %q", *seen, want)
	}
}

func TestProxyUnreachable(t *testing.T) {
	dir, _ := os.MkdirTemp("", "hd")
	defer os.RemoveAll(dir)
	rec := send(engineWith(t, SocketProxy(filepath.Join(dir, "none.sock"), "host service")), "GET", "/api/v1/network/wifi", "", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "host service unreachable") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestProxyNotConfigured(t *testing.T) {
	rec := send(engineWith(t, SocketProxy("", "host service")), "GET", "/api/v1/network/cellular", "", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "host service not configured") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestWritesAreGuarded(t *testing.T) {
	socket, seen := fakeDaemon(t)
	h := engineWith(t, SocketProxy(socket, "host service"))
	rec := send(h, "POST", "/api/v1/network/wifi/scan", `{}`,
		map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d", rec.Code)
	}
	if len(*seen) != 0 {
		t.Fatalf("a refused write reached hostd: %q", *seen)
	}
}

func TestWhatsAppForwardedToItsOwnSocket(t *testing.T) {
	hostd, hostSeen := fakeDaemon(t)
	wa, waSeen := fakeDaemon(t)
	reg, _ := channels.NewRegistry()
	h := New(Config{Login: testLogin, Channels: reg, Network: SocketProxy(hostd, "host service"), WhatsApp: SocketProxy(wa, "WhatsApp service")})

	send(h, "GET", "/api/v1/whatsapp/chats", "", nil)
	send(h, "POST", "/api/v1/whatsapp/link", "{}", sameOriginJSON)
	if rec := send(h, "POST", "/api/v1/whatsapp/link", "{}", map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", rec.Code)
	}
	if len(*waSeen) != 2 || (*waSeen)[0] != "GET /whatsapp/chats " || (*waSeen)[1] != "POST /whatsapp/link {}" {
		t.Fatalf("whatsappd saw %q", *waSeen)
	}
	if len(*hostSeen) != 0 {
		t.Fatalf("hostd saw %q", *hostSeen)
	}
	if rec := send(New(Config{Login: testLogin, Channels: reg, WhatsApp: SocketProxy("", "WhatsApp service")}), "GET", "/api/v1/whatsapp/status", "", nil); !strings.Contains(rec.Body.String(), "WhatsApp service not configured") {
		t.Fatalf("not configured: %s", rec.Body)
	}
}

func TestEmailForwarded(t *testing.T) {
	mail, seen := fakeDaemon(t)
	reg, _ := channels.NewRegistry()
	h := New(Config{Login: testLogin, Channels: reg, Email: SocketProxy(mail, "email service")})

	send(h, "GET", "/api/v1/email/threads", "", nil)
	send(h, "POST", "/api/v1/email/disconnect", `{"delete_emails":false}`, sameOriginJSON)
	if rec := send(h, "POST", "/api/v1/email/connect", "{}", map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", rec.Code)
	}
	if rec := send(h, "PUT", "/api/v1/email/connect", "{}", sameOriginJSON); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("PUT: %d", rec.Code)
	}
	if len(*seen) != 2 || (*seen)[0] != "GET /email/threads " || (*seen)[1] != `POST /email/disconnect {"delete_emails":false}` {
		t.Fatalf("emaild saw %q", *seen)
	}
}

func TestPhoneDaemonsAndSettingsForwarded(t *testing.T) {
	hostd, hostSeen := fakeDaemon(t)
	smsd, smsSeen := fakeDaemon(t)
	calld, callSeen := fakeDaemon(t)
	reg, _ := channels.NewRegistry()
	h := New(Config{Login: testLogin, Channels: reg, Network: SocketProxy(hostd, "host service"),
		SMS: SocketProxy(smsd, "SMS service"), Calls: SocketProxy(calld, "call service")})

	send(h, "GET", "/api/v1/sms/conversations", "", nil)
	send(h, "GET", "/api/v1/calls?limit=10", "", nil)
	send(h, "GET", "/api/v1/settings/retention", "", nil)
	send(h, "PUT", "/api/v1/settings/retention", `{"sms_retention_days":30,"calls_retention_days":30}`, sameOriginJSON)
	send(h, "DELETE", "/api/v1/settings/account/card", "", sameOriginJSON)
	if rec := send(h, "DELETE", "/api/v1/settings/retention", "", sameOriginJSON); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("only the card is deleted through settings: %d", rec.Code)
	}
	if rec := send(h, "GET", "/api/v1/modem/sms", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("/api/v1/modem must be gone: %d", rec.Code)
	}
	if rec := send(h, "POST", "/api/v1/sms/conversations", "{}", sameOriginJSON); rec.Code == http.StatusAccepted {
		t.Fatal("the SMS API is read-only: a POST must not reach smsd")
	}
	if len(*smsSeen) != 1 || (*smsSeen)[0] != "GET /sms/conversations " {
		t.Fatalf("smsd saw %q", *smsSeen)
	}
	if len(*callSeen) != 1 || (*callSeen)[0] != "GET /calls " {
		t.Fatalf("calld saw %q", *callSeen)
	}
	if len(*hostSeen) != 3 || (*hostSeen)[0] != "GET /settings/retention " ||
		(*hostSeen)[1] != `PUT /settings/retention {"sms_retention_days":30,"calls_retention_days":30}` ||
		(*hostSeen)[2] != "DELETE /settings/account/card " {
		t.Fatalf("hostd saw %q", *hostSeen)
	}
}

func TestTelegramForwardedToItsOwnSocket(t *testing.T) {
	tgd, seen := fakeDaemon(t)
	wa, waSeen := fakeDaemon(t)
	reg, _ := channels.NewRegistry()
	h := New(Config{Login: testLogin, Channels: reg, WhatsApp: SocketProxy(wa, "WhatsApp service"), Telegram: SocketProxy(tgd, "Telegram service")})

	send(h, "GET", "/api/v1/telegram/chats", "", nil)
	send(h, "POST", "/api/v1/telegram/link", "{}", sameOriginJSON)
	if rec := send(h, "POST", "/api/v1/telegram/link", "{}", map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", rec.Code)
	}
	if len(*seen) != 2 || (*seen)[0] != "GET /telegram/chats " || (*seen)[1] != "POST /telegram/link {}" {
		t.Fatalf("telegramd saw %q", *seen)
	}
	if len(*waSeen) != 0 {
		t.Fatalf("whatsappd saw %q", *waSeen)
	}
	if rec := send(New(Config{Login: testLogin, Channels: reg, Telegram: SocketProxy("", "Telegram service")}), "GET", "/api/v1/telegram/status", "", nil); !strings.Contains(rec.Body.String(), "Telegram service not configured") {
		t.Fatalf("not configured: %s", rec.Body)
	}
}

func TestConnectorRoutesForwardToHostd(t *testing.T) {
	socket, seen := fakeDaemon(t)
	h := engineWith(t, SocketProxy(socket, "host service"))
	send(h, "GET", "/api/v1/connectors", "", nil)
	send(h, "POST", "/api/v1/connectors/sms", "", sameOriginJSON)
	send(h, "DELETE", "/api/v1/connectors/sms", "", sameOriginJSON)
	want := []string{"GET /connectors ", "POST /connectors/sms ", "DELETE /connectors/sms "}
	if strings.Join(*seen, "|") != strings.Join(want, "|") {
		t.Fatalf("hostd saw %q", *seen)
	}
}

func TestSmsCallsRemoveForwarded(t *testing.T) {
	sms, smsSeen := fakeDaemon(t)
	calls, callsSeen := fakeDaemon(t)
	reg, _ := channels.NewRegistry()
	h := New(Config{Login: testLogin, Channels: reg, SMS: SocketProxy(sms, "SMS service"), Calls: SocketProxy(calls, "calls service")})
	send(h, "POST", "/api/v1/sms/remove", `{"delete_data":true}`, sameOriginJSON)
	send(h, "POST", "/api/v1/calls/remove", `{"delete_data":false}`, sameOriginJSON)
	send(h, "POST", "/api/v1/sms/read", `{"number":"X"}`, sameOriginJSON)
	if len(*smsSeen) != 2 || (*smsSeen)[0] != `POST /sms/remove {"delete_data":true}` || (*smsSeen)[1] != `POST /sms/read {"number":"X"}` {
		t.Fatalf("smsd saw %q", *smsSeen)
	}
	if len(*callsSeen) != 1 || (*callsSeen)[0] != `POST /calls/remove {"delete_data":false}` {
		t.Fatalf("calld saw %q", *callsSeen)
	}
}

func TestCallsSubpathsAreForwarded(t *testing.T) {
	calls, seen := fakeDaemon(t)
	reg, _ := channels.NewRegistry()
	h := New(Config{Login: testLogin, Channels: reg, Calls: SocketProxy(calls, "calls service")})
	if rec := send(h, "GET", "/api/v1/calls/7/turns", "", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("turns: %d", rec.Code)
	}
	send(h, "GET", "/api/v1/calls/7/audio?download=1", "", nil)
	if want := []string{"GET /calls/7/turns ", "GET /calls/7/audio "}; strings.Join(*seen, "|") != strings.Join(want, "|") {
		t.Fatalf("calld saw %q", *seen)
	}
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest("GET", "/api/v1/calls/7/audio", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("login must guard it: %d", unauth.Code)
	}
}

// A recording crosses the proxy byte for byte, with its headers (and a Range answer) intact.
func TestSocketProxyPassesBinaryAndHeaders(t *testing.T) {
	dir, _ := os.MkdirTemp("", "bp")
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "b.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	blob := []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0xff, 0xfe, 0x00, 0x80}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Disposition", `attachment; filename="call-7.wav"`)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(blob))
	}), Protocols: h2c.Protocols()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	reg, _ := channels.NewRegistry()
	h := New(Config{Login: testLogin, Channels: reg, Calls: SocketProxy(socket, "calls service")})
	rec := send(h, "GET", "/api/v1/calls/7/audio", "", nil)
	if !bytes.Equal(rec.Body.Bytes(), blob) || rec.Header().Get("Content-Type") != "audio/wav" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="call-7.wav"` {
		t.Fatalf("%d %v % x", rec.Code, rec.Header(), rec.Body.Bytes())
	}
	rec = send(h, "GET", "/api/v1/calls/7/audio", "", map[string]string{"Range": "bytes=4-5"})
	if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), blob[4:6]) {
		t.Fatalf("range: %d % x", rec.Code, rec.Body.Bytes())
	}
}
