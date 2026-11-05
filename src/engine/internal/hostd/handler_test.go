package hostd

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestValidation(t *testing.T) {
	s, _, _, _ := newService()
	h := NewHandler(s, nil)
	long := strings.Repeat("a", 33)
	cases := []struct{ method, path, body string }{
		{"POST", "/network/wifi/test", `{"ssid":"","password":"hunter2hunter2"}`},
		{"POST", "/network/wifi/test", `{"ssid":"` + long + `","password":"hunter2hunter2"}`},
		{"POST", "/network/wifi/test", `{"ssid":"cafe","password":"short12"}`},
		{"POST", "/network/wifi/test", `{"ssid":"cafe","password":"` + strings.Repeat("z", 64) + `"}`},
		{"POST", "/network/wifi/test", `{"ssid":"cafe","password":"tab\there12"}`},
		{"POST", "/network/wifi/test", `{"ssid":"cafe","password":"hunter2hunter2","extra":1}`},
		{"POST", "/network/wifi/test", `{"ssid":"cafe","password":"` + strings.Repeat("a", 5000) + `"}`},
		{"POST", "/network/wifi/saved", `{"ssid":"","password":""}`},
		{"POST", "/network/wifi/connect", `{"id":"not-a-uuid"}`},
		{"DELETE", "/network/wifi/saved/not-a-uuid", ``},
		{"PUT", "/network/cellular", `{"apn":""}`},
		{"PUT", "/network/cellular", `{"apn":"` + strings.Repeat("a", 63) + `"}`},
		{"PUT", "/network/cellular", `{"apn":"a b"}`},
		{"PUT", "/network/cellular", `{}`},
	}
	for _, c := range cases {
		if rec := call(h, c.method, c.path, c.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s %.60s: got %d, want 400", c.method, c.path, c.body, rec.Code)
		}
	}
	// The edges that must pass: an open network, a 64-hex key, a 62-character APN.
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/network/wifi/test", `{"ssid":"open-cafe","password":""}`},
	} {
		if rec := call(h, c.method, c.path, c.body); rec.Code != http.StatusAccepted {
			t.Errorf("%s %s: got %d, want 202", c.method, c.path, rec.Code)
		}
	}
}

func TestValidEdgesPass(t *testing.T) {
	s, _, _, _ := newService()
	h := NewHandler(s, nil)
	if rec := call(h, "PUT", "/network/cellular", `{"apn":"`+strings.Repeat("a", 62)+`"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("62-char APN: got %d", rec.Code)
	}
	s2, _, _, _ := newService()
	if rec := call(NewHandler(s2, nil), "POST", "/network/wifi/test", `{"ssid":"cafe","password":"`+strings.Repeat("ab", 32)+`"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("64-hex key: got %d", rec.Code)
	}
}

func TestStatusCodes(t *testing.T) {
	s, nm, _, _ := newService()
	h := NewHandler(s, nil)

	rec := call(h, "POST", "/network/wifi/connect", `{"id":"`+savedID+`"}`)
	var cp Checkpoint
	json.Unmarshal(rec.Body.Bytes(), &cp)
	if rec.Code != http.StatusAccepted || cp.RollbackInS != 60 || cp.ID == "" {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "DELETE", "/network/wifi/saved/"+otherID, ""); rec.Code != http.StatusConflict {
		t.Fatalf("busy: got %d", rec.Code)
	}
	if rec := call(h, "GET", "/network/checkpoints", ""); !strings.Contains(rec.Body.String(), cp.ID) {
		t.Fatalf("pending: %s", rec.Body)
	}
	if rec := call(h, "POST", "/network/checkpoints/"+cp.ID+"/confirm", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("confirm: got %d", rec.Code)
	}
	if rec := call(h, "GET", "/network/checkpoints", ""); strings.TrimSpace(rec.Body.String()) != `{"pending":null}` {
		t.Fatalf("after confirm: %s", rec.Body)
	}
	if rec := call(h, "POST", "/network/checkpoints/nope/rollback", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown checkpoint: got %d", rec.Code)
	}
	if rec := call(h, "POST", "/network/wifi/connect", `{"id":"99999999-9999-9999-9999-999999999999"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown network: got %d", rec.Code)
	}
	rec = call(h, "POST", "/network/wifi/saved", `{"ssid":"cafe","password":"hunter2hunter2"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "test it first") {
		t.Fatalf("untested save: %d %s", rec.Code, rec.Body)
	}

	nm.activation = Activation{Connected: true}
	rec = call(h, "POST", "/network/wifi/test", `{"ssid":"cafe","password":"hunter2hunter2"}`)
	var started struct{ Test string }
	json.Unmarshal(rec.Body.Bytes(), &started)
	if rec.Code != http.StatusAccepted || started.Test == "" {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	finished(t, s, started.Test)
	if rec := call(h, "GET", "/network/wifi/test/"+started.Test, ""); !strings.Contains(rec.Body.String(), `"passed"`) {
		t.Fatalf("test result: %s", rec.Body)
	}
	if rec := call(h, "GET", "/network/wifi/test/unknown", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown test: got %d", rec.Code)
	}
	if rec := call(h, "POST", "/network/wifi/saved", `{"ssid":"cafe","password":"hunter2hunter2"}`); rec.Code != http.StatusCreated {
		t.Fatalf("save: got %d %s", rec.Code, rec.Body)
	}
}

func TestAdapterErrorNeverEchoesPassword(t *testing.T) {
	s, nm, _, _ := newService()
	nm.activation = Activation{Connected: true}
	h := NewHandler(s, nil)
	finished(t, s, mustStart(t, s, "cafe", password))
	nm.err["AddWifi"] = errors.New("org.freedesktop.NetworkManager: bad psk " + password)

	rec := call(h, "POST", "/network/wifi/saved", `{"ssid":"cafe","password":"`+password+`"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), password) {
		t.Fatalf("the password reached the response: %s", rec.Body)
	}
	if strings.TrimSpace(rec.Body.String()) != `{"error":"network manager request failed"}` {
		t.Fatalf("body %s", rec.Body)
	}
}

func TestResponsesNeverContainPassword(t *testing.T) {
	s, nm, _, _ := newService()
	nm.activation = Activation{Connected: true}
	h := NewHandler(s, nil)
	body := `{"ssid":"cafe","password":"` + password + `"}`
	rec := call(h, "POST", "/network/wifi/test", body)
	var started struct{ Test string }
	json.Unmarshal(rec.Body.Bytes(), &started)
	finished(t, s, started.Test)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/network/wifi/test", body},
		{"GET", "/network/wifi/test/" + started.Test, ""},
		{"POST", "/network/wifi/saved", body},
		{"GET", "/network/wifi", ""},
		{"GET", "/network/checkpoints", ""},
		{"POST", "/network/wifi/saved", `{"ssid":"cafe","password":"` + password + `x"}`},
	} {
		if rec := call(h, c.method, c.path, c.body); strings.Contains(rec.Body.String(), password) {
			t.Errorf("%s %s leaked the password: %s", c.method, c.path, rec.Body)
		}
	}
}

// The journal is the other place an error goes: the password must not reach it either.
func TestAdapterErrorNeverLogsPassword(t *testing.T) {
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s, nm, _, _ := newService()
	nm.activation = Activation{Connected: true}
	finished(t, s, mustStart(t, s, "cafe", password))
	nm.err["AddWifi"] = errors.New("bad psk " + password)
	call(NewHandler(s, nil), "POST", "/network/wifi/saved", `{"ssid":"cafe","password":"`+password+`"}`)

	if logs.Len() == 0 {
		t.Fatal("the failure was not logged at all")
	}
	if strings.Contains(logs.String(), password) {
		t.Fatalf("the password reached the log: %s", logs.String())
	}
}

// The engine's sign-ins service reads the uplink through hostd's main socket too.
func TestUplinkIsOnTheMainSocket(t *testing.T) {
	s, nm, _, _ := newService()
	nm.uplink = "wifi"
	h := NewHandler(s, nil)
	if rec := call(h, "GET", "/uplink", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"uplink":"wifi"}` {
		t.Fatalf("uplink: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/uplink", "{}"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /uplink: %d", rec.Code)
	}
}
