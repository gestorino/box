package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func fakeCloud(t *testing.T, h http.HandlerFunc) *Cloud {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewCloud(srv.URL, "1.2.3", "example.net")
}

func reply(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

func TestClaimStartPostsTheEmail(t *testing.T) {
	var got map[string]string
	c := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/box/v1/claim/start" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		reply(w, 200, `{}`)
	})
	if err := c.ClaimStart(context.Background(), "a@b.c"); err != nil {
		t.Fatal(err)
	}
	if got["email"] != "a@b.c" {
		t.Fatalf("body %v", got)
	}
}

func TestClaimStartServerErrorIsAnError(t *testing.T) {
	c := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) { reply(w, 500, `{}`) })
	if err := c.ClaimStart(context.Background(), "a@b.c"); err == nil {
		t.Fatal("want error")
	}
}

func TestClaimFinishSuccess(t *testing.T) {
	var got map[string]string
	c := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/box/v1/claim/finish" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		reply(w, 200, `{"name":"abcdefghij","box_domain":"example.net","relay":"relay.example.net:7000"}`)
	})
	cl, err := c.ClaimFinish(context.Background(), "a@b.c", "123456", "box-1", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	want := ClaimResult{Name: "abcdefghij", BoxDomain: "example.net", Relay: "relay.example.net:7000"}
	if cl != want {
		t.Fatalf("got %+v", cl)
	}
	if got["email"] != "a@b.c" || got["code"] != "123456" || got["box_id"] != "box-1" || got["secret"] != "s3cret" {
		t.Fatalf("body %v", got)
	}
}

func TestClaimFinishErrorsMap(t *testing.T) {
	cases := []struct {
		code int
		body string
		want error
	}{
		{400, `{"error":"wrong code"}`, ErrWrongCode},
		{400, `{"error":"expired code"}`, ErrExpiredCode},
		{400, `{"error":"no code"}`, ErrNoCode},
		{409, `{"error":"box already claimed"}`, ErrAlreadyClaimed},
	}
	for _, tc := range cases {
		c := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) { reply(w, tc.code, tc.body) })
		_, err := c.ClaimFinish(context.Background(), "a@b.c", "1", "b", "s3cret")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v", tc.body, err)
		}
	}
	c := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) { reply(w, 400, `{"error":"other"}`) })
	_, err := c.ClaimFinish(context.Background(), "a@b.c", "1", "b", "s3cret")
	if err == nil || errors.Is(err, ErrWrongCode) {
		t.Fatalf("got %v", err)
	}
	c = fakeCloud(t, func(w http.ResponseWriter, r *http.Request) { reply(w, 500, `{}`) })
	if _, err := c.ClaimFinish(context.Background(), "a@b.c", "1", "b", "s3cret"); err == nil {
		t.Fatal("want error")
	}
}

func TestErrorsNeverCarryTheSecretOrEmail(t *testing.T) {
	c := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) { reply(w, 500, `{"error":"s3cret a@b.c"}`) })
	_, err := c.ClaimFinish(context.Background(), "a@b.c", "1", "b", "s3cret")
	if err == nil || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "a@b.c") {
		t.Fatalf("got %v", err)
	}
	_, err = c.Status(context.Background(), "b", "s3cret")
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("got %v", err)
	}
	dead := NewCloud("http://127.0.0.1:1", "1", "example.net")
	err = dead.ClaimStart(context.Background(), "a@b.c")
	if err == nil || strings.Contains(err.Error(), "a@b.c") {
		t.Fatalf("got %v", err)
	}
}

func TestStatus(t *testing.T) {
	var hdr http.Header
	code := 200
	body := `{"claimed":true,"name":"abcdefghij"}`
	c := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header
		if r.Method != "GET" || r.URL.Path != "/box/v1/status" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		reply(w, code, body)
	})
	ok, err := c.Status(context.Background(), "box-1", "s3cret")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if hdr.Get("X-Box-Id") != "box-1" || hdr.Get("X-Box-Secret") != "s3cret" || hdr.Get("X-Box-Version") != "1.2.3" {
		t.Fatalf("headers %v", hdr)
	}
	code = 401
	ok, err = c.Status(context.Background(), "box-1", "s3cret")
	if err != nil || ok {
		t.Fatalf("401: %v %v", ok, err)
	}
	code = 200
	body = `{"claimed":false}`
	if ok, err = c.Status(context.Background(), "box-1", "s3cret"); err == nil || ok {
		t.Fatalf("200 not claimed: %v %v", ok, err)
	}
	body = `nonsense`
	if ok, err = c.Status(context.Background(), "box-1", "s3cret"); err == nil || ok {
		t.Fatalf("unparseable: %v %v", ok, err)
	}
	code = 500
	ok, err = c.Status(context.Background(), "box-1", "s3cret")
	if err == nil || ok {
		t.Fatalf("500: %v %v", ok, err)
	}
}

func TestClaimFinishRefusesAnAnswerOutsideTheBoxDomain(t *testing.T) {
	// The box hands its secret to the relay it is told of: only its own domain's relay will do.
	for _, body := range []string{
		`{"name":"abcdefghij","box_domain":"example.org","relay":"relay.example.org:7000"}`,
		`{"name":"abcdefghij","box_domain":"example.net","relay":"relay.example.org:7000"}`,
		`{"name":"abcdefghij","box_domain":"example.net","relay":"relay.example.net:7001"}`,
		`{"name":"","box_domain":"example.net","relay":"relay.example.net:7000"}`,
		`{"name":"a.b","box_domain":"example.net","relay":"relay.example.net:7000"}`,
		`{"name":"A-b","box_domain":"example.net","relay":"relay.example.net:7000"}`,
		`{"name":"-ab","box_domain":"example.net","relay":"relay.example.net:7000"}`,
	} {
		c := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) { reply(w, 200, body) })
		if res, err := c.ClaimFinish(context.Background(), "a@b.c", "1", "b", "s3cret"); err == nil {
			t.Errorf("%s: accepted as %+v", body, res)
		}
	}
}

func TestAnUnreachableControlPlaneIsErrUnreachable(t *testing.T) {
	dead := NewCloud("http://127.0.0.1:1", "1", "example.net")
	if err := dead.ClaimStart(context.Background(), "a@b.c"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("claim start: %v", err)
	}
	if _, err := dead.ClaimFinish(context.Background(), "a@b.c", "1", "b", "s"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("claim finish: %v", err)
	}
	c := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) { reply(w, 500, `{}`) })
	if err := c.ClaimStart(context.Background(), "a@b.c"); err == nil || errors.Is(err, ErrUnreachable) {
		t.Fatalf("a 500 is an answer, not unreachable: %v", err)
	}
}

// A redirect is an answer, never followed: following one would hand X-Box-Secret (or a re-POSTed
// email, code and secret) to its target, and a target answering 401 would wipe the box.
func TestRedirectsAreNeverFollowed(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		reply(w, 401, `{}`)
	}))
	t.Cleanup(target.Close)
	for _, code := range []int{301, 302, 303, 307, 308} {
		c := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+r.URL.Path, code)
		})
		ok, err := c.Status(context.Background(), "box-1", "s3cret")
		if err == nil || ok {
			t.Errorf("status %d: %v %v; want an error, never removal", code, ok, err)
		}
		if _, err := c.ClaimFinish(context.Background(), "a@b.c", "1", "b", "s3cret"); err == nil {
			t.Errorf("claim finish %d: want an error", code)
		}
		if err := c.ClaimStart(context.Background(), "a@b.c"); err == nil {
			t.Errorf("claim start %d: want an error", code)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the redirect's target was contacted %d times", n)
	}
}
