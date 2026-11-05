package hostsettings

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"concierge/engine/internal/h2c"
)

// serve serves h as hostd's settings socket does: unencrypted HTTP/2 only.
func serve(t *testing.T, h http.HandlerFunc) (socket string, stop func()) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "ps")
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket = filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, Protocols: h2c.Protocols()}
	go srv.Serve(ln)
	return socket, func() { srv.Close() }
}

func TestClientReadsAndKeepsLastKnown(t *testing.T) {
	var reads atomic.Int32
	socket, stop := serve(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.Write([]byte(`{"sms_retention_days":30,"calls_retention_days":0}`))
	})
	c := NewClient(socket, 5*time.Millisecond)
	if got, known := c.Retention(); known {
		t.Fatalf("known before the first read: %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for got, _ := c.Retention(); got != (Retention{SMSDays: 30, CallsDays: 0}); got, _ = c.Retention() {
		if time.Now().After(deadline) {
			t.Fatalf("never read: %+v", got)
		}
		time.Sleep(time.Millisecond)
	}
	stop() // hostd goes away
	n := reads.Load()
	time.Sleep(30 * time.Millisecond)
	if reads.Load() != n {
		t.Fatal("server should be gone")
	}
	if got, known := c.Retention(); !known || got != (Retention{SMSDays: 30, CallsDays: 0}) {
		t.Fatalf("after hostd went away: %+v %v (want the last known value)", got, known)
	}
}

func TestSettingsUnreachableKeepsLastKnown(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "none.sock"), 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	c.Run(ctx)
	if got, known := c.Retention(); known {
		t.Fatalf("hostd never reachable, yet known: %+v (nothing may be purged on a guess)", got)
	}
}

func TestClientUplink(t *testing.T) {
	socket, stop := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/uplink" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"uplink":"wifi"}`))
	})
	c := NewClient(socket, time.Minute)
	if got, err := c.Uplink(context.Background()); err != nil || got != "wifi" {
		t.Fatalf("uplink %q %v", got, err)
	}
	stop() // hostd goes away
	if got, err := c.Uplink(context.Background()); err == nil {
		t.Fatalf("hostd gone, yet %q", got)
	}
}

func TestCallsKeepIsUnknownUntilHostdAnswers(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "none.sock"), 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	c.Run(ctx)
	if got, known := c.CallsKeep(); known {
		t.Fatalf("hostd never reachable, yet known: %+v (a call may not be kept on a guess)", got)
	}
}

func TestCallsKeepHoldsTheLastValueWhenHostdGoesAway(t *testing.T) {
	socket, stop := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"sms_retention_days":30,"calls_keep_transcript":true,"calls_keep_audio":false,"calls_debug_days":90}`))
	})
	want := CallsKeep{Transcript: true, DebugDays: 90}
	c := NewClient(socket, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for got, _ := c.CallsKeep(); got != want; got, _ = c.CallsKeep() {
		if time.Now().After(deadline) {
			t.Fatalf("never read: %+v", got)
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	time.Sleep(30 * time.Millisecond)
	if got, known := c.CallsKeep(); !known || got != want {
		t.Fatalf("after hostd went away: %+v %v", got, known)
	}
	if r, _ := c.Retention(); r.SMSDays != 30 {
		t.Fatalf("retention: %+v", r)
	}
}

func TestRefreshReadsTheStoredValueNow(t *testing.T) {
	var transcript atomic.Bool
	socket, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if transcript.Load() {
			w.Write([]byte(`{"calls_keep_transcript":true,"calls_debug_days":7}`))
			return
		}
		w.Write([]byte(`{"calls_debug_days":7}`))
	})
	c := NewClient(socket, time.Hour) // the ticker never fires within the test
	c.Refresh(context.Background())
	if got, known := c.CallsKeep(); !known || got != (CallsKeep{DebugDays: 7}) {
		t.Fatalf("first refresh: %+v %v", got, known)
	}
	transcript.Store(true) // the owner turns the switch on
	c.Refresh(context.Background())
	if got, _ := c.CallsKeep(); got != (CallsKeep{Transcript: true, DebugDays: 7}) {
		t.Fatalf("after the switch changed: %+v (want the new value at once)", got)
	}
}

func TestVoiceIsUnknownUntilHostdAnswers(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "none.sock"), 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	c.Run(ctx)
	if got, known := c.Voice(); known {
		t.Fatalf("hostd never reachable, yet known: %+v", got)
	}
}

func TestOwnerIsUnknownUntilHostdAnswers(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "none.sock"), 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	c.Run(ctx)
	if got, known := c.Owner(); known {
		t.Fatalf("hostd never reachable, yet known: %+v", got)
	}
}
