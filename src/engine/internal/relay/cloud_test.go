package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeCloud(t *testing.T, h http.HandlerFunc) *Cloud {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewCloud(srv.URL, "1.2.3")
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
	dead := NewCloud("http://127.0.0.1:1", "1")
	err = dead.ClaimStart(context.Background(), "a@b.c")
	if err == nil || strings.Contains(err.Error(), "a@b.c") {
		t.Fatalf("got %v", err)
	}
}

func TestStatus(t *testing.T) {
	var hdr http.Header
	code := 200
	c := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header
		if r.Method != "GET" || r.URL.Path != "/box/v1/status" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		reply(w, code, `{"claimed":true,"name":"abcdefghij"}`)
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
	code = 500
	ok, err = c.Status(context.Background(), "box-1", "s3cret")
	if err == nil || ok {
		t.Fatalf("500: %v %v", ok, err)
	}
}
