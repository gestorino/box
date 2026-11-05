package secrets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"concierge/engine/internal/h2c"
)

// serve runs the handler on a real Unix socket and returns a client for it.
func serve(t *testing.T, callers map[uint32]string) (*http.Client, *Store) {
	t.Helper()
	st, _ := open(t, key(1))
	dir, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: NewHandler(st, callers), ConnContext: ConnContext, Protocols: h2c.Protocols()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &http.Client{Transport: h2c.Transport(sock)}, st
}

func us() map[uint32]string { return map[uint32]string{uint32(os.Getuid()): "engine"} }

func do(t *testing.T, c *http.Client, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://x"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestRoundTrip(t *testing.T) {
	c, st := serve(t, us())
	if code, _ := do(t, c, "GET", "/secrets/openrouter", ""); code != 404 {
		t.Fatalf("absent: %d", code)
	}
	if code, _ := do(t, c, "PUT", "/secrets/signins/a.com/password", `{"value":"pw"}`); code != 204 {
		t.Fatalf("put: %d", code)
	}
	do(t, c, "PUT", "/secrets/signins/b.com/password", `{"value":"pw2"}`)
	do(t, c, "PUT", "/secrets/openrouter", `{"value":"k"}`)
	code, body := do(t, c, "GET", "/secrets/signins/a.com/password", "")
	var got struct{ Value string }
	json.Unmarshal([]byte(body), &got)
	if code != 200 || got.Value != "pw" {
		t.Fatalf("get: %d %q", code, body)
	}
	// the namespace is the caller's
	if v, ok, _ := st.Get(ctx, "engine", "openrouter"); !ok || v != "k" {
		t.Fatalf("stored under engine: %q %v", v, ok)
	}
	if code, _ := do(t, c, "GET", "/secrets", ""); code != 404 {
		t.Fatalf("no listing: %d", code)
	}
	if code, _ := do(t, c, "DELETE", "/secrets/openrouter", ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := do(t, c, "GET", "/secrets/openrouter", ""); code != 404 {
		t.Fatalf("after delete: %d", code)
	}
}

func TestUnknownUIDForbidden(t *testing.T) {
	c, _ := serve(t, map[uint32]string{uint32(os.Getuid()) + 1: "other"})
	for _, r := range [][3]string{
		{"GET", "/secrets/x", ""}, {"PUT", "/secrets/x", `{"value":"v"}`},
		{"DELETE", "/secrets/x", ""}, {"GET", "/elsewhere", ""},
	} {
		code, body := do(t, c, r[0], r[1], r[2])
		if code != 403 || !strings.Contains(body, `"error":"not a caller"`) {
			t.Errorf("%s %s: %d %q", r[0], r[1], code, body)
		}
	}
}

func TestBadNames(t *testing.T) {
	c, _ := serve(t, us())
	for _, n := range []string{"../x", "A", "a//b", "a/./b", "a/..", "a/", strings.Repeat("a", 201), "a:b", "a%20b"} {
		if code, _ := do(t, c, "PUT", "/secrets/"+n, `{"value":"v"}`); code != 400 {
			t.Errorf("PUT %q: %d", n, code)
		}
		if code, _ := do(t, c, "GET", "/secrets/"+n, ""); code != 400 {
			t.Errorf("GET %q: %d", n, code)
		}
	}
	if code, _ := do(t, c, "PUT", "/secrets/"+strings.Repeat("a", 200), `{"value":"v"}`); code != 204 {
		t.Errorf("200 bytes: %d", code)
	}
}

func TestBadValues(t *testing.T) {
	c, _ := serve(t, us())
	for name, body := range map[string]string{
		"number":   `{"value":1}`,
		"null":     `{"value":null}`,
		"missing":  `{}`,
		"empty":    `{"value":""}`,
		"not json": `nope`,
		"extra":    `{"value":"v","x":1}`,
		"too big":  `{"value":"` + strings.Repeat("a", 16385) + `"}`,
		"huge":     `{"value":"` + strings.Repeat("a", 100000) + `"}`,
	} {
		if code, _ := do(t, c, "PUT", "/secrets/x", body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _ := do(t, c, "PUT", "/secrets/x", `{"value":"`+strings.Repeat("a", 16384)+`"}`); code != 204 {
		t.Errorf("16 KiB: %d", code)
	}
}

func TestDoesNotOpen(t *testing.T) {
	var lb logBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lb, nil)))
	defer slog.SetDefault(old)
	c, st := serve(t, us())
	do(t, c, "PUT", "/secrets/x", `{"value":"v"}`)
	if _, err := st.db.Exec(`UPDATE secrets SET sealed = ?`, []byte("garbage-garbage-garbage-garbage-garbage")); err != nil {
		t.Fatal(err)
	}
	code, body := do(t, c, "GET", "/secrets/x", "")
	if code != 500 || !strings.Contains(body, `"error":"secret does not open"`) {
		t.Fatalf("%d %q", code, body)
	}
	// a lost or re-minted master key is diagnosable from the journal: which secret, whose
	if want := `level=ERROR msg=secrets caller=engine op=read name=x err="secret does not open"`; !strings.Contains(lb.String(), want) {
		t.Fatalf("log lacks %q:\n%s", want, lb.String())
	}
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestValueNeverLogged(t *testing.T) {
	var lb logBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lb, nil)))
	defer slog.SetDefault(old)
	c, _ := serve(t, us())
	const secret = "hunter2-very-secret"
	do(t, c, "PUT", "/secrets/pw", `{"value":"`+secret+`"}`)
	do(t, c, "GET", "/secrets/pw", "")
	do(t, c, "PUT", "/secrets/Bad", `{"value":"`+secret+`"}`)
	do(t, c, "PUT", "/secrets/pw", `{"value":"`+secret+`",`)
	do(t, c, "DELETE", "/secrets/pw", "")
	out := lb.String()
	if strings.Contains(out, secret) {
		t.Fatalf("value in log:\n%s", out)
	}
	for _, want := range []string{"caller=engine", "op=write", "op=read", "op=delete", "name=pw"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

// A handler served without ConnContext knows no peer: refused, even with uid 0 among the callers.
func TestNoPeerIsRefused(t *testing.T) {
	st, _ := open(t, key(1))
	rec := httptest.NewRecorder()
	NewHandler(st, map[uint32]string{0: "engine"}).ServeHTTP(rec, httptest.NewRequest("GET", "/secrets/x", nil))
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"error":"not a caller"`) {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
}

// A refusal is logged with the uid that asked (or unknown), never the name it asked for.
func TestRefusalsAreLoggedWithoutTheName(t *testing.T) {
	var lb logBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lb, nil)))
	defer slog.SetDefault(old)
	c, st := serve(t, map[uint32]string{uint32(os.Getuid()) + 1: "other"})
	do(t, c, "GET", "/secrets/hidden-name", "")
	NewHandler(st, map[uint32]string{0: "engine"}).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PUT", "/secrets/hidden-name", nil))
	out := lb.String()
	for _, want := range []string{"op=refused", fmt.Sprintf("uid=%d", os.Getuid()), "uid=unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hidden-name") {
		t.Fatalf("name in log:\n%s", out)
	}
}
