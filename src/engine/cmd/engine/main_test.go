package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"concierge/engine/internal/secrets"
	"concierge/engine/internal/secrets/secretstest"
	"concierge/engine/internal/signins"
)

// A run's event before the hub is set is dropped, not a crash; -agent-db needs what the hub reads.
func TestTheForwarderDropsEventsBeforeTheHub(t *testing.T) {
	(&eventForwarder{}).Event(signins.RunEvent{Run: 1, Kind: "done"})
	if _, _, err := startConverse(context.Background(), converseConfig{DB: "x.db"}, nil, &eventForwarder{}); err == nil {
		t.Fatal("-agent-db without its sockets started")
	}
	if _, _, err := startConverse(context.Background(), converseConfig{DB: "x.db", TimeZone: "Bad/Zone"}, nil, &eventForwarder{}); err == nil || !strings.Contains(err.Error(), "-time-zone") {
		t.Fatalf("a bad time zone: %v", err)
	}
	h, wait, err := startConverse(context.Background(), converseConfig{}, nil, &eventForwarder{})
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("no agents handler without -agent-db: the agents' sign-in still answers")
	}
	wait()
}

// No secretd, no engine: it never falls back to keeping a secret anywhere else.
func TestTheEngineNeedsSecretd(t *testing.T) {
	if _, err := secretsClient(""); err == nil || !strings.Contains(err.Error(), "-secrets-socket") {
		t.Fatalf("err = %v", err)
	}
	if c, err := secretsClient("/run/concierge-secrets/s"); c == nil || err != nil {
		t.Fatalf("%v %v", c, err)
	}
}

// Every run's gate reads the AI page's key from secretd, and a secretd that doesn't answer is its
// error, never "no key".
func TestTheKeyGateReadsSecretd(t *testing.T) {
	ctx := context.Background()
	sc := secretstest.Start(t, "engine")
	if has, err := hasKey(sc)(ctx); has || err != nil {
		t.Fatalf("none kept: %v %v", has, err)
	}
	if err := sc.Put(ctx, "openrouter", "sk-or-v1-test"); err != nil {
		t.Fatal(err)
	}
	if has, err := hasKey(sc)(ctx); !has || err != nil {
		t.Fatalf("kept: %v %v", has, err)
	}
	if _, err := hasKey(secretstest.Down(t))(ctx); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("down: %v", err)
	}
}

// The live view's stream and a run's recording go on for as long as they last: the server sets no
// write or idle-write deadline of its own, so neither is cut off.
func TestTheServerSetsNoWriteTimeout(t *testing.T) {
	if srv := newServer(context.Background(), "", http.NotFoundHandler()); srv.WriteTimeout != 0 || srv.ReadTimeout != 0 {
		t.Fatalf("write timeout %v, read timeout %v", srv.WriteTimeout, srv.ReadTimeout)
	}
}

// A request that runs until its client goes (the live view's stream) ends when the engine stops:
// Shutdown waits for every connection to go idle, and a stream never does on its own.
func TestRequestsEndWhenTheEngineStops(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	entered, ended := make(chan struct{}), make(chan struct{})
	srv := newServer(ctx, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(ended)
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()
	go http.Get("http://" + ln.Addr().String() + "/")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never arrived")
	}
	stop()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the request outlived the engine")
	}
}

// The catalog is the box's own, grown through the API: nothing is loaded at start.
func TestOpenSignInsStartsWithTheStoresOwnCards(t *testing.T) {
	dir, err := os.MkdirTemp("", "si")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	svc, store, err := openSignIns(context.Background(), filepath.Join(dir, "s.db"), nil,
		secretstest.Start(t, "engine"), "", "", "", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sites, err := svc.Sites(context.Background())
	if err != nil || len(sites) != 0 {
		t.Fatalf("sites = %v, %v", sites, err)
	}
}
