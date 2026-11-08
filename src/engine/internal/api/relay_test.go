package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"concierge/engine/internal/channels"
)

func relayEngine(t *testing.T, socket string) http.Handler {
	reg, _ := channels.NewRegistry(stubChannel("email"))
	return New(Config{Login: testLogin, Channels: reg, Relay: SocketProxy(socket, "remote access")})
}

func sendFrom(h http.Handler, peer, via, method, path, body string) (int, string) {
	r := httptestRequest(method, path, body, peer, via)
	rec := serve(h, r)
	return rec.Code, rec.Body.String()
}

func TestViaRelayNeedsALoopbackPeer(t *testing.T) {
	for peer, want := range map[string]bool{
		"127.0.0.1:5": true, "127.9.9.9:5": true, "[::1]:5": true,
		"192.0.2.1:5": false, "10.0.0.2:5": false, "[2001:db8::1]:5": false, "garbage": false,
	} {
		r := httptestRequest("GET", "/", "", peer, "relay")
		if got := viaRelay(r); got != want {
			t.Errorf("%s: %v, want %v", peer, got, want)
		}
	}
	if viaRelay(httptestRequest("GET", "/", "", "127.0.0.1:5", "")) {
		t.Error("no header counted")
	}
}

func TestIssuingDevicesIsRefusedOverTheRelay(t *testing.T) {
	socket, seen := fakeDaemon(t)
	h := relayEngine(t, socket)
	code, body := sendFrom(h, "127.0.0.1:5", "relay", "POST", "/api/v1/relay/devices", `{}`)
	if code != http.StatusForbidden || !strings.Contains(body, "issue devices from home") {
		t.Fatalf("got %d %s", code, body)
	}
	if len(*seen) != 0 {
		t.Fatalf("relayd saw %q", *seen)
	}
	if code, _ := sendFrom(h, "127.0.0.1:5", "relay", "POST", "/api/v1/relay/devices/", `{}`); code != http.StatusForbidden {
		t.Errorf("trailing slash: %d", code)
	}
	// The mux redirects unclean paths itself; either way relayd is not reached.
	for _, p := range []string{"/api/v1/relay/devices//", "/api/v1/relay/x/../devices"} {
		if code, _ := sendFrom(h, "127.0.0.1:5", "relay", "POST", p, `{}`); code == http.StatusAccepted {
			t.Errorf("%s: %d", p, code)
		}
	}
	if len(*seen) != 0 {
		t.Fatalf("relayd saw %q", *seen)
	}
	if code, _ := sendFrom(h, "127.0.0.1:5", "relay", "GET", "/api/v1/relay/devices", ""); code != http.StatusAccepted {
		t.Fatalf("listing over the relay: %d", code)
	}
}

func TestTheRelayHeaderFromANonLoopbackPeerIsIgnored(t *testing.T) {
	socket, seen := fakeDaemon(t)
	h := relayEngine(t, socket)
	if code, _ := sendFrom(h, "192.0.2.1:5", "relay", "POST", "/api/v1/relay/devices", `{}`); code != http.StatusAccepted {
		t.Fatalf("got %d", code)
	}
	if code, _ := sendFrom(h, "192.0.2.1:5", "", "DELETE", "/api/v1/relay/devices/7", ""); code != http.StatusAccepted {
		t.Fatalf("delete: %d", code)
	}
	want := []string{"POST /relay/devices {}", "DELETE /relay/devices/7 "}
	if len(*seen) != 2 || (*seen)[0] != want[0] || (*seen)[1] != want[1] {
		t.Fatalf("relayd saw %q", *seen)
	}
}

func TestNoRelaySocketAnswers503(t *testing.T) {
	code, body := sendFrom(relayEngine(t, ""), "192.0.2.1:5", "", "GET", "/api/v1/relay/status", "")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "remote access not configured") {
		t.Fatalf("got %d %s", code, body)
	}
}

func TestStatusSaysWhetherTheRequestCameOverTheRelay(t *testing.T) {
	h := relayEngine(t, "")
	if _, body := sendFrom(h, "127.0.0.1:5", "relay", "GET", "/api/v1/status", ""); !strings.Contains(body, `"via_relay":true`) {
		t.Fatalf("over the relay: %s", body)
	}
	if _, body := sendFrom(h, "192.0.2.1:5", "relay", "GET", "/api/v1/status", ""); !strings.Contains(body, `"via_relay":false`) {
		t.Fatalf("from the LAN: %s", body)
	}
}

func httptestRequest(method, path, body, peer, via string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "192.0.2.10"
	r.RemoteAddr = peer
	r.SetBasicAuth("helper", "helper")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if via != "" {
		r.Header.Set("X-Concierge-Via", via)
	}
	return r
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}
