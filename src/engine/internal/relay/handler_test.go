package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"

	"concierge/engine/internal/h2c"
	"concierge/engine/internal/secrets"
	"concierge/engine/internal/secrets/secretstest"
)

// ctlAnswers is the fake control plane's answers to the claim, set by the test.
type ctlAnswers struct {
	mu         sync.Mutex
	start      int
	finish     int
	finishBody string
}

func (a *ctlAnswers) set(finish int, body string) {
	a.mu.Lock()
	a.finish, a.finishBody = finish, body
	a.mu.Unlock()
}

// apiRig is relayd's socket API over a real Service, a real secretd and a fake control plane
// speaking the /box/v1 contract, reached through h2c on a Unix socket as the engine reaches it.
type apiRig struct {
	c      *http.Client
	store  *Store
	tunnel *fakeTunnel
	ctl    *ctlAnswers

	mu     sync.Mutex
	closed []string // the serials whose connections the front was told to close
}

const goodClaim = `{"name":"abcdefghij","box_domain":"example.net","relay":"relay.example.net:7000"}`

func newAPIRig(t *testing.T, sec *secrets.Client, cloud *Cloud) *apiRig {
	t.Helper()
	r := &apiRig{tunnel: &fakeTunnel{}, ctl: &ctlAnswers{start: 200, finish: 200, finishBody: goodClaim}}
	if cloud == nil {
		cloud = fakeCloud(t, func(w http.ResponseWriter, req *http.Request) {
			r.ctl.mu.Lock()
			defer r.ctl.mu.Unlock()
			switch req.URL.Path {
			case "/box/v1/claim/start":
				reply(w, r.ctl.start, `{}`)
			case "/box/v1/claim/finish":
				reply(w, r.ctl.finish, r.ctl.finishBody)
			case "/box/v1/status":
				reply(w, 200, `{"claimed":true}`)
			}
		})
	}
	st, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r.store = st
	ca, err := LoadCA(context.Background(), secretstest.Start(t, "relay"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, cloud, sec, r.tunnel, func() string { return "certificate" }, time.Now)
	h := NewHandler(svc, ca, func(serial string) {
		r.mu.Lock()
		r.closed = append(r.closed, serial)
		r.mu.Unlock()
	})

	dir, err := os.MkdirTemp("", "rl") // short: macOS caps socket paths at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, Protocols: h2c.Protocols(), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	r.c = &http.Client{Transport: h2c.Transport(sock), Timeout: 10 * time.Second}
	return r
}

// do sends one request and returns the status, the headers and the body.
func (r *apiRig) do(t *testing.T, method, path, body string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://relayd"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := r.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func (r *apiRig) want(t *testing.T, method, path, body string, code int, answer string) {
	t.Helper()
	got, _, b := r.do(t, method, path, body)
	if got != code || strings.TrimSpace(b) != answer {
		t.Fatalf("%s %s %s: %d %s, want %d %s", method, path, body, got, b, code, answer)
	}
}

func (r *apiRig) status(t *testing.T) map[string]string {
	t.Helper()
	code, _, b := r.do(t, "GET", "/relay/status", "")
	if code != http.StatusOK {
		t.Fatalf("status: %d %s", code, b)
	}
	var st map[string]string
	if err := json.Unmarshal([]byte(b), &st); err != nil {
		t.Fatalf("status %s: %v", b, err)
	}
	return st
}

func (r *apiRig) claim(t *testing.T) {
	t.Helper()
	r.want(t, "POST", "/relay/claim/start", `{"email":"owner@example.org"}`, 204, "")
	r.want(t, "POST", "/relay/claim/finish", `{"code":"123456"}`, 204, "")
}

func TestAPIStatusUnclaimed(t *testing.T) {
	r := newAPIRig(t, secretstest.Start(t, "relay"), nil)
	want := map[string]string{"state": "unclaimed", "name": "", "address": "", "waiting": "", "problem": ""}
	if got := r.status(t); !mapsEqual(got, want) {
		t.Fatalf("status %v, want %v", got, want)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func TestAPIClaimStartThenFinish(t *testing.T) {
	r := newAPIRig(t, secretstest.Start(t, "relay"), nil)
	r.want(t, "POST", "/relay/claim/start", `{"email":"owner@example.org"}`, 204, "")
	if st := r.status(t); st["state"] != "claiming" {
		t.Fatalf("after start: %v", st)
	}
	r.want(t, "POST", "/relay/claim/finish", `{"code":"123456"}`, 204, "")
	want := map[string]string{"state": "claimed", "name": "abcdefghij", "address": "abcdefghij.example.net",
		"waiting": "certificate", "problem": ""}
	if got := r.status(t); !mapsEqual(got, want) {
		t.Fatalf("status %v, want %v", got, want)
	}
	if starts, _ := r.tunnel.counts(); starts != 1 {
		t.Fatalf("tunnel started %d times", starts)
	}
}

func TestAPIClaimStartWithSecretdDown(t *testing.T) {
	r := newAPIRig(t, secretstest.Down(t), nil)
	r.want(t, "POST", "/relay/claim/start", `{"email":"owner@example.org"}`, 502, `{"error":"secrets unavailable"}`)
}

func TestAPIClaimStartWithTheControlPlaneUnreachable(t *testing.T) {
	r := newAPIRig(t, secretstest.Start(t, "relay"), NewCloud("http://127.0.0.1:1", "1", "example.net"))
	r.want(t, "POST", "/relay/claim/start", `{"email":"owner@example.org"}`, 503, `{"error":"control plane unreachable"}`)
	if st := r.status(t); st["state"] != "unclaimed" {
		t.Fatalf("state %v", st)
	}
}

func TestAPIClaimFinishRefusals(t *testing.T) {
	r := newAPIRig(t, secretstest.Start(t, "relay"), nil)
	r.want(t, "POST", "/relay/claim/start", `{"email":"owner@example.org"}`, 204, "")
	for _, msg := range []string{"wrong code", "expired code", "no code"} {
		r.ctl.set(400, `{"error":"`+msg+`"}`)
		r.want(t, "POST", "/relay/claim/finish", `{"code":"1"}`, 400, `{"error":"`+msg+`"}`)
		if st := r.status(t); st["state"] != "claiming" {
			t.Fatalf("after %q: %v", msg, st)
		}
	}
	r.ctl.set(409, `{"error":"box already claimed"}`)
	r.want(t, "POST", "/relay/claim/finish", `{"code":"1"}`, 409, `{"error":"box already claimed"}`)
}

func TestAPIClaimFinishWithoutStartIsAConflict(t *testing.T) {
	r := newAPIRig(t, secretstest.Start(t, "relay"), nil)
	r.want(t, "POST", "/relay/claim/finish", `{"code":"123456"}`, 409, `{"error":"no claim in progress"}`)
}

func TestAPIClaimStartWhenClaimedIsAConflict(t *testing.T) {
	r := newAPIRig(t, secretstest.Start(t, "relay"), nil)
	r.claim(t)
	r.want(t, "POST", "/relay/claim/start", `{"email":"owner@example.org"}`, 409, `{"error":"box already claimed"}`)
}

func TestAPIRefusesBadBodies(t *testing.T) {
	r := newAPIRig(t, secretstest.Start(t, "relay"), nil)
	for _, c := range []struct{ path, body string }{
		{"/relay/claim/start", ``},
		{"/relay/claim/start", `{"email":""}`},
		{"/relay/claim/start", `{"email":"no-at-sign"}`},
		{"/relay/claim/start", `{"email":"a@b.c","extra":1}`},
		{"/relay/claim/finish", `{"code":""}`},
		{"/relay/devices", `{"label":""}`},
		{"/relay/devices", `{"label":"   "}`},
		{"/relay/devices", `{"label":"` + strings.Repeat("x", 65) + `"}`},
	} {
		r.want(t, "POST", c.path, c.body, 400, `{"error":"bad request"}`)
	}
}

func TestAPIIssuesDevicesOnlyWhenClaimed(t *testing.T) {
	r := newAPIRig(t, secretstest.Start(t, "relay"), nil)
	r.want(t, "GET", "/relay/devices", "", 200, `[]`)
	r.want(t, "POST", "/relay/devices", `{"label":"Owner's phone"}`, 409, `{"error":"box not claimed"}`)
	if ds, err := r.store.Devices(context.Background()); err != nil || len(ds) != 0 {
		t.Fatalf("devices %v %v", ds, err)
	}
}

func TestAPIIssueListRevoke(t *testing.T) {
	r := newAPIRig(t, secretstest.Start(t, "relay"), nil)
	r.claim(t)
	code, hdr, b := r.do(t, "POST", "/relay/devices", `{"label":"Owner's phone"}`)
	if code != http.StatusOK {
		t.Fatalf("issue: %d %s", code, b)
	}
	if hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("issue's Cache-Control %q: the password and the key must not be kept", hdr.Get("Cache-Control"))
	}
	var issued struct {
		Serial   string    `json:"serial"`
		P12      string    `json:"p12"`
		Password string    `json:"password"`
		Expires  time.Time `json:"expires"`
	}
	if err := json.Unmarshal([]byte(b), &issued); err != nil {
		t.Fatal(err)
	}
	der, err := base64.StdEncoding.DecodeString(issued.P12)
	if err != nil {
		t.Fatalf("p12 is not base64: %v", err)
	}
	_, leaf, _, err := pkcs12.DecodeChain(der, issued.Password)
	if err != nil {
		t.Fatalf("the .p12 doesn't open with its password: %v", err)
	}
	if leaf.SerialNumber.Text(16) != issued.Serial || leaf.Subject.CommonName != "Owner's phone" {
		t.Fatalf("leaf serial %s CN %q, answered serial %s", leaf.SerialNumber.Text(16), leaf.Subject.CommonName, issued.Serial)
	}
	if !leaf.NotAfter.Equal(issued.Expires) {
		t.Fatalf("expires %v, the certificate's %v", issued.Expires, leaf.NotAfter)
	}
	if !r.store.ActiveSerial(issued.Serial, time.Now()) {
		t.Fatal("the issued device is not active")
	}

	code, _, b = r.do(t, "GET", "/relay/devices", "")
	var list []struct {
		Serial  string    `json:"serial"`
		Label   string    `json:"label"`
		Issued  time.Time `json:"issued"`
		Expires time.Time `json:"expires"`
	}
	if err := json.Unmarshal([]byte(b), &list); code != 200 || err != nil {
		t.Fatalf("list: %d %s %v", code, b, err)
	}
	if len(list) != 1 || list[0].Serial != issued.Serial || list[0].Label != "Owner's phone" ||
		time.Since(list[0].Issued) > time.Minute || !list[0].Expires.Equal(issued.Expires) {
		t.Fatalf("list %+v", list)
	}
	if strings.Contains(b, issued.Password) || strings.Contains(b, "p12") {
		t.Fatalf("the list carries the password or the file: %s", b)
	}

	r.want(t, "DELETE", "/relay/devices/"+issued.Serial, "", 204, "")
	if r.store.ActiveSerial(issued.Serial, time.Now()) {
		t.Fatal("a revoked device is still active")
	}
	r.mu.Lock()
	closed := append([]string(nil), r.closed...)
	r.mu.Unlock()
	if len(closed) != 1 || closed[0] != issued.Serial {
		t.Fatalf("the front closed %v, want [%s]", closed, issued.Serial)
	}
	r.want(t, "GET", "/relay/devices", "", 200, `[]`)
}

// captureLogs sends slog's default output to a buffer until the test's cleanup.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug - 8})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestAPILogsNoEmailCodeOrPassword(t *testing.T) {
	logs := captureLogs(t)
	r := newAPIRig(t, secretstest.Start(t, "relay"), nil)
	r.ctl.mu.Lock()
	r.ctl.start = 500
	r.ctl.mu.Unlock()
	code, _, _ := r.do(t, "POST", "/relay/claim/start", `{"email":"owner@example.org"}`)
	if code < 500 {
		t.Fatalf("claim start against a failing control plane: %d", code)
	}
	r.ctl.mu.Lock()
	r.ctl.start = 200
	r.ctl.mu.Unlock()
	r.want(t, "POST", "/relay/claim/start", `{"email":"owner@example.org"}`, 204, "")
	r.ctl.set(500, `{"error":"owner@example.org 987654"}`)
	if code, _, _ := r.do(t, "POST", "/relay/claim/finish", `{"code":"987654"}`); code < 500 {
		t.Fatalf("claim finish against a failing control plane: %d", code)
	}
	r.ctl.set(200, goodClaim)
	r.want(t, "POST", "/relay/claim/finish", `{"code":"987654"}`, 204, "")
	_, _, b := r.do(t, "POST", "/relay/devices", `{"label":"Owner's phone"}`)
	var issued struct{ P12, Password string }
	if err := json.Unmarshal([]byte(b), &issued); err != nil || issued.Password == "" {
		t.Fatalf("issue: %s %v", b, err)
	}
	out := logs.String()
	for _, s := range []string{"owner@example.org", "987654", issued.Password, issued.P12[:32]} {
		if strings.Contains(out, s) {
			t.Fatalf("the log carries %q:\n%s", s, out)
		}
	}
}

// A claim the control plane accepts is the box's even when the console went away meanwhile: the
// finish runs on past its request.
func TestAPIClaimFinishOutlivesItsRequest(t *testing.T) {
	arrived, release, gone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cloud := fakeCloud(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/box/v1/claim/start":
			reply(w, 200, `{}`)
		case "/box/v1/claim/finish":
			close(arrived)
			select {
			case <-req.Context().Done(): // the box gave up on the call
				close(gone)
				return
			case <-release:
			}
			reply(w, 200, goodClaim)
		case "/box/v1/status":
			reply(w, 200, `{"claimed":true}`)
		}
	})
	r := newAPIRig(t, secretstest.Start(t, "relay"), cloud)
	r.want(t, "POST", "/relay/claim/start", `{"email":"owner@example.org"}`, 204, "")

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "POST", "http://relayd/relay/claim/finish", strings.NewReader(`{"code":"123456"}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		if resp, err := r.c.Do(req); err == nil {
			resp.Body.Close()
		}
		close(done)
	}()
	<-arrived
	cancel() // the browser goes away mid-call
	<-done
	select { // time for the cancellation to reach the box's call, if it would
	case <-gone:
		t.Fatal("the browser going away cancelled the claim's call to the control plane")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		id, err := r.store.Identity(context.Background())
		if err == nil && id.State == Claimed && id.Name == "abcdefghij" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the accepted claim was lost: %+v %v", id, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
