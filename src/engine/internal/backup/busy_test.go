package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func screensRoot(t *testing.T, sites ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "run/concierge-worker/screens")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range sites {
		if err := os.Mkdir(filepath.Join(dir, s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestBusyIdle(t *testing.T) {
	got, err := Busy(&fakeSystem{}, screensRoot(t))
	if err != nil || got != "" {
		t.Fatalf("Busy = %q, %v; want idle", got, err)
	}
}

func TestBusyNoScreensDirIsIdle(t *testing.T) {
	got, err := Busy(&fakeSystem{}, t.TempDir())
	if err != nil || got != "" {
		t.Fatalf("Busy = %q, %v; want idle", got, err)
	}
}

func TestBusyNamesTheRun(t *testing.T) {
	got, err := Busy(&fakeSystem{}, screensRoot(t, "example.org"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "a run is in progress at example.org") {
		t.Fatalf("Busy = %q", got)
	}
	if strings.Contains(got, "restart the worker") {
		t.Fatalf("worker is active, yet Busy = %q", got)
	}
}

func TestBusyNamesTheLeftoverScreen(t *testing.T) {
	sys := &fakeSystem{active: map[string]bool{"concierge-worker.service": false}}
	got, err := Busy(sys, screensRoot(t, "example.org"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "example.org") || !strings.Contains(got, "restart the worker") {
		t.Fatalf("Busy = %q", got)
	}
}

func TestBusyCall(t *testing.T) {
	sys := &fakeSystem{callsOut: "/org/freedesktop/ModemManager1/Call/3 (active)"}
	got, err := Busy(sys, screensRoot(t))
	if err != nil || got != "a call is in progress" {
		t.Fatalf("Busy = %q, %v", got, err)
	}
}

func TestBusyCallsError(t *testing.T) {
	sys := &fakeSystem{callsErr: os.ErrPermission}
	if _, err := Busy(sys, screensRoot(t)); err == nil {
		t.Fatal("Busy hid a Calls failure")
	}
}

func TestBusyNoModemIsNoCall(t *testing.T) {
	for _, out := range []string{
		"error: couldn't find modem",
		"error: no modems were found",
		"No calls were found",
		"",
	} {
		if calls := parseCalls(out); len(calls) != 0 {
			t.Errorf("parseCalls(%q) = %v; want none", out, calls)
		}
	}
	if !isNoModem("error: couldn't find modem") {
		t.Error("isNoModem missed mmcli's no-modem error")
	}
	if isNoModem("error: couldn't connect to ModemManager") {
		t.Error("isNoModem hid a real failure")
	}
}
