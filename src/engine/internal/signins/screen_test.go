package signins

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWebsockify listens on <dir>/<site>/web.sock: /vnc.html as a plain GET, and on an upgrade a 101
// that echoes bytes. seen counts the requests that reached it.
func fakeWebsockify(t *testing.T, site string) (screens string, seen *atomic.Int32) {
	return fakeWebsockifyAuth(t, site, new(atomic.Int32))
}

func fakeWebsockifyAuth(t *testing.T, site string, auth *atomic.Int32) (screens string, seen *atomic.Int32) {
	t.Helper()
	screens, err := os.MkdirTemp("", "scr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(screens) })
	if err := os.Mkdir(filepath.Join(screens, site), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(screens, site, "web.sock"))
	if err != nil {
		t.Fatal(err)
	}
	seen = new(atomic.Int32)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if r.Header.Get("Authorization") != "" {
			auth.Add(1)
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			buf.Flush()
			io.Copy(conn, buf) // echo
			return
		}
		io.WriteString(w, "noVNC "+r.URL.Path)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return screens, seen
}

func screenFixture(t *testing.T) (*handlerFixture, *atomic.Int32) {
	f := newHandlerFixture(t)
	screens, seen := fakeWebsockify(t, shopKey)
	f.svc.cfg.ScreensDir = screens
	f.svc.cfg.NoVNCDir = fakeNoVNC(t)
	return f, seen
}

// fakeNoVNC is the box's novnc package: vnc.html, with a file beside the directory that must not be
// served.
func fakeNoVNC(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "novnc")
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		filepath.Join(dir, "vnc.html"):     "the package's vnc.html",
		filepath.Join(dir, "app", "ui.js"): "the package's ui.js",
		filepath.Join(root, "secret"):      "not noVNC",
	} {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func (f *handlerFixture) get(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

// screenPath is noVNC's page, served by the engine; wsPath is the run's websocket, proxied.
const (
	screenPath = "/api/v1/signins/sites/shop.test/screen/vnc.html"
	wsPath     = "/api/v1/signins/sites/shop.test/screen/websockify"
)

func TestScreenServesNoVNCFromThePackage(t *testing.T) {
	f, seen := screenFixture(t)
	// No run: noVNC's files are the box's public package, behind the console login.
	for path, want := range map[string]string{screenPath: "the package's vnc.html",
		"/api/v1/signins/sites/shop.test/screen/app/ui.js": "the package's ui.js"} {
		if rec := f.get(path); rec.Code != 200 || rec.Body.String() != want {
			t.Fatalf("%s: got %d %q", path, rec.Code, rec.Body)
		}
	}
	if seen.Load() != 0 {
		t.Fatal("websockify was asked for noVNC's files")
	}
}

func TestScreenServesNoVNCBeforeTheRunsSocketIsUp(t *testing.T) {
	// A run is claimed before the worker has started its screen: the page must still load,
	// and the console's view keeps trying the socket.
	f, _ := screenFixture(t)
	if err := os.Remove(filepath.Join(f.svc.cfg.ScreensDir, shopKey, "web.sock")); err != nil {
		t.Fatal(err)
	}
	holdForARun(t, f.svc, shopKey)
	if rec := f.get(screenPath); rec.Code != 200 || rec.Body.String() != "the package's vnc.html" {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
	if rec := f.get(wsPath); rec.Code != 502 {
		t.Fatalf("websocket: got %d", rec.Code)
	}
}

func TestScreenServesNothingOutsideThePackage(t *testing.T) {
	f, _ := screenFixture(t)
	for _, path := range []string{"..%2fsecret", "%2e%2e/secret", "app/..%2f..%2fsecret"} {
		rec := f.get("/api/v1/signins/sites/shop.test/screen/" + path)
		if strings.Contains(rec.Body.String(), "not noVNC") {
			t.Fatalf("%s: served the file outside: %d", path, rec.Code)
		}
	}
}

// holdForARun is a run in flight on the site: its phase held and its id current, as startRun leaves it.
func holdForARun(t *testing.T, svc *Service, site string) {
	t.Helper()
	if !svc.claim(site, phaseErrand) {
		t.Fatal("claim")
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.current[site] = 1
}

// The screen is the run's page's to show for the whole run, not only a wait: the worker alone
// switches the box's input, and only in a wait.
func TestTheScreenIsOpenWhileARunHoldsTheSite(t *testing.T) {
	f, seen := screenFixture(t)
	holdForARun(t, f.svc, shopKey)
	rec := f.get(wsPath)
	if rec.Code != 200 || rec.Body.String() != "noVNC /websockify" || seen.Load() != 1 {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
}

func TestAClaimThatIsNotARunOpensNoScreen(t *testing.T) {
	f, seen := screenFixture(t)
	if !f.svc.claim(shopKey, phaseRemoving) {
		t.Fatal("claim")
	}
	if rec := f.get(wsPath); rec.Code != 404 || seen.Load() != 0 {
		t.Fatalf("removing: %d, seen %d", rec.Code, seen.Load())
	}
}

func TestScreenWebsocketWhileWaiting(t *testing.T) {
	for _, code := range []bool{false, true} {
		f, _ := screenFixture(t)
		holdForARun(t, f.svc, shopKey)
		phase, kind := phaseWaiting, waitCaptcha
		if code {
			phase, kind = phaseWaitingCode, waitCode
		}
		f.svc.openWait(shopKey, &ownerWait{answer: make(chan bool, 1), kind: kind}, phase)
		if rec := f.get(wsPath); rec.Code != 200 {
			t.Fatalf("code=%v: got %d", code, rec.Code)
		}
	}
}

func TestScreenWebsocketIs404WithoutARun(t *testing.T) {
	f, seen := screenFixture(t)
	if rec := f.get(wsPath); rec.Code != 404 {
		t.Fatalf("no run: %d", rec.Code)
	}
	holdForARun(t, f.svc, shopKey)
	f.svc.release(shopKey)
	if rec := f.get(wsPath); rec.Code != 404 {
		t.Fatalf("run over: %d", rec.Code)
	}
	if seen.Load() != 0 {
		t.Fatal("the screen was reached")
	}
}

func TestScreenIs404WithoutScreensDir(t *testing.T) {
	f, _ := screenFixture(t)
	f.svc.cfg.ScreensDir = ""
	holdForARun(t, f.svc, shopKey)
	for _, path := range []string{screenPath, wsPath} {
		if rec := f.get(path); rec.Code != 404 {
			t.Fatalf("%s: got %d", path, rec.Code)
		}
	}
}

func TestScreenIs404ForANonSlugSite(t *testing.T) {
	f, _ := screenFixture(t)
	for _, site := range []string{"A.B", "a_b", "%2e%2e", "x%20y"} {
		un, _ := url.PathUnescape(site)
		holdForARun(t, f.svc, un)
		for _, file := range []string{"vnc.html", "websockify"} {
			if rec := f.get("/api/v1/signins/sites/" + site + "/screen/" + file); rec.Code != 404 {
				t.Fatalf("%s/%s: got %d", site, file, rec.Code)
			}
		}
	}
}

func upgrade(t *testing.T, f *handlerFixture, origin string) (*http.Response, net.Conn, *bufio.Reader) {
	t.Helper()
	srv := httptest.NewServer(f.h)
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	req := "GET /api/v1/signins/sites/shop.test/screen/websockify HTTP/1.1\r\nHost: " + srv.Listener.Addr().String() +
		"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"
	if origin == "same" {
		origin = "http://" + srv.Listener.Addr().String()
	}
	if origin != "" {
		req += "Origin: " + origin + "\r\n"
	}
	conn.Write([]byte(req + "\r\n"))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp, conn, br
}

func TestScreenBridgesTheWebsocket(t *testing.T) {
	f, _ := screenFixture(t)
	holdForARun(t, f.svc, shopKey)
	resp, conn, br := upgrade(t, f, "same")
	if resp.StatusCode != 101 {
		t.Fatalf("got %d", resp.StatusCode)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write([]byte("ping"))
	got := make([]byte, 4)
	if _, err := io.ReadFull(br, got); err != nil || string(got) != "ping" {
		t.Fatalf("echo: %q %v", got, err)
	}
}

func TestScreenRefusesACrossSiteUpgrade(t *testing.T) {
	for _, origin := range []string{"http://evil.example", ""} {
		f, seen := screenFixture(t)
		holdForARun(t, f.svc, shopKey)
		resp, _, _ := upgrade(t, f, origin)
		if resp.StatusCode != 403 {
			t.Fatalf("origin %q: got %d", origin, resp.StatusCode)
		}
		if seen.Load() != 0 {
			t.Fatal("the screen saw the request")
		}
	}
}

func TestSettingsSayWhetherThisComputerHasScreens(t *testing.T) {
	// A capability, not a run's state: on the box the view frames the screen from the moment a run is
	// started, before the worker has started it.
	f, _ := screenFixture(t)
	if got := f.svc.Settings(t.Context()); !got.Screens {
		t.Fatalf("with a screens dir: %+v", got)
	}
	f.svc.cfg.ScreensDir = ""
	if got := f.svc.Settings(t.Context()); got.Screens {
		t.Fatal("screens without a screens dir")
	}
}

func TestScreenRefusesAPost(t *testing.T) {
	f, seen := screenFixture(t)
	holdForARun(t, f.svc, shopKey)
	for _, path := range []string{screenPath, wsPath} {
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, httptest.NewRequest("POST", path, nil))
		if rec.Code != 405 || seen.Load() != 0 {
			t.Fatalf("%s: got %d, seen %d", path, rec.Code, seen.Load())
		}
	}
}

func TestScreenCannotBeFramedByAnotherPage(t *testing.T) {
	f, _ := screenFixture(t)
	want := "frame-ancestors 'self'"
	check := func(name, path string, code int) {
		t.Helper()
		if rec := f.get(path); rec.Code != code || rec.Header().Get("Content-Security-Policy") != want {
			t.Fatalf("%s: %d %q", name, rec.Code, rec.Header())
		}
	}
	check("websocket, no run", wsPath, 404)
	check("noVNC, no run", screenPath, 200)
	check("noVNC, missing file", "/api/v1/signins/sites/shop.test/screen/nothing.js", 404)
	holdForARun(t, f.svc, shopKey)
	check("websocket, run in flight", wsPath, 200)
	r := httptest.NewRequest("GET", wsPath, nil)
	r.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	if rec.Code != 403 || rec.Header().Get("Content-Security-Policy") != want {
		t.Fatalf("403: %d %q", rec.Code, rec.Header())
	}
	if err := os.RemoveAll(f.svc.cfg.ScreensDir); err != nil {
		t.Fatal(err)
	}
	check("websocket, no socket", wsPath, 502)
	f.svc.cfg.ScreensDir = ""
	check("no screens", screenPath, 404)
}

func TestScreenRefusesAnyUpgradeFromAnotherOrigin(t *testing.T) {
	f, seen := screenFixture(t)
	holdForARun(t, f.svc, shopKey)
	for _, path := range []string{screenPath, wsPath} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Upgrade", "websocket, h2c")
		r.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, r)
		if rec.Code != 403 || seen.Load() != 0 {
			t.Fatalf("%s: got %d, seen %d", path, rec.Code, seen.Load())
		}
	}
}

func TestScreenKeepsTheLoginFromWebsockify(t *testing.T) {
	f := newHandlerFixture(t)
	auth := new(atomic.Int32)
	screens, seen := fakeWebsockifyAuth(t, shopKey, auth)
	f.svc.cfg.ScreensDir = screens
	holdForARun(t, f.svc, shopKey)
	r := httptest.NewRequest("GET", wsPath, nil)
	r.SetBasicAuth("dev", "dev")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	if rec.Code != 200 || seen.Load() != 1 || auth.Load() != 0 {
		t.Fatalf("got %d, seen %d, auth %d", rec.Code, seen.Load(), auth.Load())
	}
}

// silentWebsockify listens on the site's web.sock and never accepts: the kernel completes the
// connection, the request is written, and no answer comes.
func silentWebsockify(t *testing.T, f *handlerFixture) {
	t.Helper()
	sock := filepath.Join(f.svc.cfg.ScreensDir, shopKey, "web.sock")
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
}

func TestScreenGivesUpOnAWebsockifyThatNeverAnswers(t *testing.T) {
	if screenTransport.ResponseHeaderTimeout <= 0 {
		t.Fatal("the screen's proxy waits forever for websockify's answer")
	}
	f, _ := screenFixture(t)
	silentWebsockify(t, f)
	holdForARun(t, f.svc, shopKey)
	was := screenTransport.ResponseHeaderTimeout
	screenTransport.ResponseHeaderTimeout = 100 * time.Millisecond
	t.Cleanup(func() { screenTransport.ResponseHeaderTimeout = was })
	start := time.Now()
	if rec := f.get(wsPath); rec.Code != 502 {
		t.Fatalf("got %d", rec.Code)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("waited past the timeout")
	}
}

func TestScreenDialHasATimeout(t *testing.T) {
	if screenDialTimeout <= 0 {
		t.Fatal("the screen's proxy dials without a timeout")
	}
}
