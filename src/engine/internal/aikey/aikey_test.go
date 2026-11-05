package aikey

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKey = "sk-or-v1-0123456789abcdefa1b2"

// fakeOpenRouter answers /key and /credits as OpenRouter does, for one good key.
type fakeOpenRouter struct {
	keyStatus      int
	limitRemaining string // JSON: null or a number
	creditsStatus  int
	sawAuth        []string
}

func (f *fakeOpenRouter) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.sawAuth = append(f.sawAuth, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"User not found.","code":401}}`))
			return
		}
		switch r.URL.Path {
		case "/api/v1/key":
			if f.keyStatus != 0 {
				w.WriteHeader(f.keyStatus)
				return
			}
			w.Write([]byte(`{"data":{"label":"sk-or-v1-012...","limit":null,"limit_remaining":` + f.limitRemaining + `,"usage":66.94}}`))
		case "/api/v1/credits":
			if f.creditsStatus != 0 {
				w.WriteHeader(f.creditsStatus)
				return
			}
			w.Write([]byte(`{"data":{"total_credits":70,"total_usage":66.94}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func checker(srv *httptest.Server) *Checker {
	c := NewChecker()
	c.BaseURL = srv.URL + "/api/v1"
	return c
}

func TestCheckReadsTheKeysLimitLeft(t *testing.T) {
	f := &fakeOpenRouter{limitRemaining: "12.4"}
	credit, err := checker(f.server(t)).Check(context.Background(), testKey)
	if err != nil || credit == nil || *credit != 12.4 {
		t.Fatalf("Check: %v %v", credit, err)
	}
	if len(f.sawAuth) != 1 {
		t.Fatalf("calls %d, want only /key when it says the limit left", len(f.sawAuth))
	}
}

func TestCheckWithoutALimitReadsTheAccountsCredit(t *testing.T) {
	f := &fakeOpenRouter{limitRemaining: "null"}
	credit, err := checker(f.server(t)).Check(context.Background(), testKey)
	if err != nil || credit == nil || *credit != 3.06 {
		t.Fatalf("Check: %v %v, want 70 - 66.94 = 3.06", credit, err)
	}
}

func TestCheckWorksWithoutACreditItCannotRead(t *testing.T) {
	f := &fakeOpenRouter{limitRemaining: "null", creditsStatus: http.StatusForbidden}
	credit, err := checker(f.server(t)).Check(context.Background(), testKey)
	if err != nil || credit != nil {
		t.Fatalf("Check: %v %v, want ok with no credit", credit, err)
	}
}

func TestCheckSaysARefusedKey(t *testing.T) {
	f := &fakeOpenRouter{limitRemaining: "1"}
	_, err := checker(f.server(t)).Check(context.Background(), "sk-or-v1-wrong")
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err %v, want ErrRefused", err)
	}
	f = &fakeOpenRouter{keyStatus: http.StatusForbidden}
	if _, err := checker(f.server(t)).Check(context.Background(), testKey); !errors.Is(err, ErrRefused) {
		t.Fatalf("403: err %v, want ErrRefused", err)
	}
}

func TestCheckSaysOpenRouterCouldNotBeReached(t *testing.T) {
	f := &fakeOpenRouter{keyStatus: http.StatusBadGateway}
	if _, err := checker(f.server(t)).Check(context.Background(), testKey); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("5xx: err %v, want ErrUnreachable", err)
	}
	srv := (&fakeOpenRouter{}).server(t)
	c := checker(srv)
	srv.Close()
	if _, err := c.Check(context.Background(), testKey); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("closed: err %v, want ErrUnreachable", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(slow.Close)
	c = checker(slow)
	c.Client.Timeout = 50 * time.Millisecond
	_, err := c.Check(context.Background(), testKey)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("timeout: err %v, want ErrUnreachable", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatal("the error carries the key")
	}
}
