package backup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const busyCall = "/org/freedesktop/ModemManager1/Call/1 incoming (active)"

// box is a temp root deployed at 0.2.3 (abc1234), header()'s build, with some live state.
func box(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	put(t, root, "opt/concierge/VERSION", "0.2.3\n", 0o644)
	put(t, root, "opt/concierge/REVISION", "abc1234\n", 0o644)
	put(t, root, "var/lib/concierge/signins.db", "live", 0o600)
	return root
}

func stops() string  { return strings.Join(Units, " ") }
func starts() string { return strings.Join(CoreUnits, " ") }

// markerGoneAtStart fails t when Start finds the marker still there: the units' condition on it
// would keep them from starting.
func markerGoneAtStart(t *testing.T, sys *fakeSystem, root string) {
	sys.onStart = func() {
		if _, err := os.Stat(filepath.Join(root, Marker)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the marker is still there when the services start: %v", err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// callsAtWrite is an io.Writer that remembers what the system had been asked when the archive's
// first byte arrived.
type callsAtWrite struct {
	sys   *fakeSystem
	seen  []string
	wrote bool
	bytes.Buffer
}

func (w *callsAtWrite) Write(p []byte) (int, error) {
	if !w.wrote {
		w.seen, w.wrote = append([]string(nil), w.sys.calls...), true
	}
	return w.Buffer.Write(p)
}

func TestCreateStopsWritesStarts(t *testing.T) {
	root := box(t)
	sys := &fakeSystem{}
	out := &callsAtWrite{sys: sys}
	h, err := Create(sys, root, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Calls", "Stop " + stops()}; !reflect.DeepEqual(out.seen, want) {
		t.Fatalf("calls when the archive was written = %q, want %q", out.seen, want)
	}
	if want := []string{"Calls", "Stop " + stops(), "Start " + starts()}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("calls = %q, want %q", sys.calls, want)
	}
	got, err := ReadHeader(&out.Buffer)
	if err != nil {
		t.Fatal(err)
	}
	host, _ := os.Hostname()
	if got.Version != "0.2.3" || got.Revision != "abc1234" || got.Format != 1 || got.Box != host {
		t.Fatalf("header = %+v", got)
	}
	if len(got.ID) != 32 || got.ID != h.ID || got.TakenAt.IsZero() {
		t.Fatalf("header ID %q (returned %q), taken at %v", got.ID, h.ID, got.TakenAt)
	}
	if !reflect.DeepEqual(got.Paths, []string{"var/lib/concierge"}) {
		t.Fatalf("paths = %v", got.Paths)
	}
	if _, err := os.Stat(filepath.Join(root, Marker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker written without stop: %v", err)
	}
	h2, err := Create(&fakeSystem{}, root, &bytes.Buffer{}, false)
	if err != nil || h2.ID == h.ID {
		t.Fatalf("second backup's ID %q, first %q, %v", h2.ID, h.ID, err)
	}
}

func TestCreateRefusesWhenBusy(t *testing.T) {
	root := box(t)
	sys := &fakeSystem{callsOut: busyCall}
	var out bytes.Buffer
	if _, err := Create(sys, root, &out, false); err == nil || !strings.Contains(err.Error(), "a call is in progress") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(sys.calls, []string{"Calls"}) {
		t.Fatalf("calls = %q, want only the busy check", sys.calls)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %d bytes", out.Len())
	}
}

// With stop the services stay stopped: the marker's condition on every unit keeps them so, reboot
// included.
func TestCreateStopLeavesStopped(t *testing.T) {
	root := box(t)
	sys := &fakeSystem{}
	h, err := Create(sys, root, &bytes.Buffer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Calls", "Stop " + stops()}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("calls = %q, want %q", sys.calls, want)
	}
	m := filepath.Join(root, Marker)
	if got := readFile(t, m); got != h.ID {
		t.Fatalf("marker = %q, want %q", got, h.ID)
	}
	if fi, _ := os.Stat(m); fi.Mode().Perm() != 0o600 {
		t.Fatalf("marker mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(m)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("marker dir mode %v", fi.Mode().Perm())
	}
}

func TestCreateWithoutRecoverySaysSo(t *testing.T) {
	root := box(t)
	var out bytes.Buffer
	h, err := Create(&fakeSystem{}, root, &out, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadHeader(&out); h.Recovery || got.Recovery {
		t.Fatalf("no recovery file, yet Recovery: returned %v, archived %v", h.Recovery, got.Recovery)
	}

	put(t, root, RecoveryFile, "locked", 0o600)
	out.Reset()
	if h, err = Create(&fakeSystem{}, root, &out, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadHeader(&out); !h.Recovery || !got.Recovery {
		t.Fatalf("recovery file present, yet Recovery: returned %v, archived %v", h.Recovery, got.Recovery)
	}
}

func TestResume(t *testing.T) {
	root := box(t)
	put(t, root, Marker, header().ID, 0o600)
	sys := &fakeSystem{}
	markerGoneAtStart(t, sys, root)
	if err := Resume(sys, root); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Start " + starts()}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("calls = %q, want %q", sys.calls, want)
	}
	if _, err := os.Stat(filepath.Join(root, Marker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker still there: %v", err)
	}
	// Resuming a box that was not stopped for a backup is not an error.
	if err := Resume(&fakeSystem{}, root); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryCodeLocksTheTPMKey(t *testing.T) {
	t.Parallel()
	root := box(t)
	put(t, root, credentialFile, "tpm blob", 0o600)
	put(t, root, "var/lib/concierge-secrets/secrets.db", "sealed", 0o600)
	k := bytes.Repeat([]byte{7}, 32)
	sys := &fakeSystem{key: k}

	code1, replaced, err := RecoveryCode(sys, root)
	if err != nil || replaced {
		t.Fatalf("first: replaced %v, %v", replaced, err)
	}
	if want := []string{"Decrypt " + filepath.Join(root, credentialFile)}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("calls = %q, want %q", sys.calls, want)
	}
	file := filepath.Join(root, RecoveryFile)
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
		t.Fatalf("recovery file mode %v", fi.Mode().Perm())
	}
	if got, err := Unlock([]byte(readFile(t, file)), code1); err != nil || !bytes.Equal(got, k) {
		t.Fatalf("unlock with the code: %v", err)
	}

	code2, replaced, err := RecoveryCode(sys, root)
	if err != nil || !replaced || code2 == code1 {
		t.Fatalf("second: replaced %v, same code %v, %v", replaced, code2 == code1, err)
	}
	locked := []byte(readFile(t, file))
	if got, err := Unlock(locked, code2); err != nil || !bytes.Equal(got, k) {
		t.Fatalf("unlock with the new code: %v", err)
	}
	if _, err := Unlock(locked, code1); !errors.Is(err, ErrWrongCode) {
		t.Fatalf("the old code: %v, want ErrWrongCode", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(file))
	if len(entries) != 2 {
		t.Fatalf("the store's directory holds %v: a temp file left behind", entries)
	}
}
