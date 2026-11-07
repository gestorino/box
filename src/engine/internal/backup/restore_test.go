package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// masterKey is fresh each time: what a restore wipes is never another test's.
func masterKey() []byte { return bytes.Repeat([]byte{9}, 32) }

// The recovery file every restore test shares: one scrypt Lock for them all.
var (
	lockOnce            sync.Once
	testCode, otherCode string
	testLocked          []byte
)

func lockedKey(t *testing.T) (code string, locked []byte) {
	t.Helper()
	lockOnce.Do(func() {
		testCode, _ = NewCode()
		otherCode, _ = NewCode()
		testLocked, _ = Lock(masterKey(), testCode)
	})
	if testLocked == nil {
		t.Fatal("locking the test key failed")
	}
	return testCode, testLocked
}

// backupState is what each backup in these tests holds, by path.
var backupState = map[string]string{
	"var/lib/concierge/signins.db":         "backed up",
	"var/lib/concierge-secrets/secrets.db": "sealed",
	"etc/concierge/engine.env":             "login",
	credentialFile:                         "old tpm blob",
}

// backupFile writes header()'s backup of backupState (and of the recovery file when locked is not
// nil) and returns its path.
func backupFile(t *testing.T, locked []byte) string {
	t.Helper()
	src := t.TempDir()
	for p, body := range backupState {
		put(t, src, p, body, 0o600)
	}
	if locked != nil {
		put(t, src, RecoveryFile, string(locked), 0o600)
	}
	var buf bytes.Buffer
	if err := Write(&buf, header(), src); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := os.WriteFile(f, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// stoppedBox is a box stopped for header()'s backup, its live state other than the backup's.
func stoppedBox(t *testing.T) string {
	t.Helper()
	root := box(t)
	for p := range backupState {
		put(t, root, p, "live", 0o600)
	}
	put(t, root, Marker, header().ID, 0o600)
	return root
}

// testID is header()'s ID: the backup's, and the .old- copies' suffix.
var testID = header().ID

func staged(root, rel string) string {
	return filepath.Join(root, "var/lib/concierge-backup/staging-"+header().ID, rel)
}

func noSleep(n *int) func(time.Duration) {
	return func(d time.Duration) {
		if d != time.Second {
			panic("polled at " + d.String())
		}
		*n++
	}
}

func refuseCode(called *bool) func() (string, error) {
	return func() (string, error) {
		*called = true
		return "", errors.New("no code here")
	}
}

func assertState(t *testing.T, root string, want func(path string) string) {
	t.Helper()
	for p := range backupState {
		if got := readFile(t, filepath.Join(root, p)); got != want(p) {
			t.Fatalf("%s = %q, want %q", p, got, want(p))
		}
	}
}

func restored(p string) string { return backupState[p] }
func untouched(string) string  { return "live" }

// assertNoLeftovers: no staging directory, and no .old- copy beside any backup path.
func assertNoLeftovers(t *testing.T, root string) {
	t.Helper()
	if es, _ := filepath.Glob(filepath.Join(root, "var/lib/concierge-backup/staging-*")); len(es) != 0 {
		t.Fatalf("staging left: %v", es)
	}
	for _, p := range Paths {
		if es, _ := filepath.Glob(filepath.Join(root, p+".old-*")); len(es) != 0 {
			t.Fatalf("old copy left: %v", es)
		}
	}
}

// withoutActive drops the poll's Active calls.
func withoutActive(calls []string) []string {
	return slices.DeleteFunc(slices.Clone(calls), func(c string) bool { return strings.HasPrefix(c, "Active ") })
}

func TestRestoreSameBox(t *testing.T) {
	root := stoppedBox(t)
	sys := &fakeSystem{key: masterKey()}
	markerGoneAtStart(t, sys, root)
	var asked bool
	var sleeps int
	rep, err := Restore(sys, root, backupFile(t, nil), RestoreOptions{ReadCode: refuseCode(&asked), sleep: noSleep(&sleeps)})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.SameBox || rep.Header.ID != header().ID || asked || sleeps != 0 || rep.Leftovers != nil {
		t.Fatalf("report %+v, code asked %v, slept %d", rep, asked, sleeps)
	}
	want := []string{
		"Calls",
		"Decrypt " + staged(root, credentialFile),
		"Calls", // busy again, right before stopping
		"Stop " + stops(),
		"Start " + starts(),
	}
	if got := withoutActive(sys.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	// Only the core's services: hostd starts the added connectors itself, and stops the rest.
	for _, u := range Units {
		core := slices.Contains(CoreUnits, u) && strings.HasSuffix(u, ".service")
		if polled := slices.Contains(sys.calls, "Active "+u); polled != core {
			t.Fatalf("%s: polled %v", u, polled)
		}
	}
	assertState(t, root, restored)
	if _, err := os.Stat(filepath.Join(root, Marker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker still there: %v", err)
	}
	assertNoLeftovers(t, root)
}

func TestRestoreNamesServicesThatDidNotStart(t *testing.T) {
	root := stoppedBox(t)
	// The email connector is not added (hostd keeps it stopped): not the restore's to wait for.
	sys := &fakeSystem{key: masterKey(), active: map[string]bool{
		"concierge-worker.service": false,
		"concierge-email.service":  false,
	}}
	var sleeps int
	_, err := Restore(sys, root, backupFile(t, nil), RestoreOptions{sleep: noSleep(&sleeps)})
	if err == nil || !strings.Contains(err.Error(), "concierge-worker.service") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "concierge-engine.service") || strings.Contains(err.Error(), "concierge-email.service") {
		t.Fatalf("names a service it should not: %v", err)
	}
	if sleeps != 60 {
		t.Fatalf("polled over %d s, want 60", sleeps)
	}
	assertState(t, root, restored)
	assertNoLeftovers(t, root)
}

func TestRestoreRefusesOtherVersion(t *testing.T) {
	root := stoppedBox(t)
	put(t, root, "opt/concierge/REVISION", "def5678\n", 0o644)
	sys := &fakeSystem{key: masterKey()}
	_, err := Restore(sys, root, backupFile(t, nil), RestoreOptions{})
	if err == nil || !strings.Contains(err.Error(), "abc1234") || !strings.Contains(err.Error(), "def5678") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(sys.calls, []string{"Calls"}) {
		t.Fatalf("calls = %q", sys.calls)
	}
	assertState(t, root, untouched)
	if got := readFile(t, filepath.Join(root, Marker)); got != header().ID {
		t.Fatalf("marker = %q", got)
	}
	assertNoLeftovers(t, root)
}

func TestRestoreRefusesStaleWithoutForce(t *testing.T) {
	for name, marker := range map[string]string{"no marker": "", "another backup's": "ffffffffffffffffffffffffffffffff"} {
		t.Run(name, func(t *testing.T) {
			root := stoppedBox(t)
			m := filepath.Join(root, Marker)
			if marker == "" {
				os.Remove(m)
			} else {
				put(t, root, Marker, marker, 0o600)
			}
			b := backupFile(t, nil)
			sys := &fakeSystem{key: masterKey()}
			_, err := Restore(sys, root, b, RestoreOptions{sleep: noSleep(new(int))})
			if err == nil || !strings.Contains(err.Error(), "FORCE=1") {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(sys.calls, []string{"Calls"}) {
				t.Fatalf("calls = %q", sys.calls)
			}
			assertState(t, root, untouched)

			if _, err := Restore(&fakeSystem{key: masterKey()}, root, b, RestoreOptions{Force: true, sleep: noSleep(new(int))}); err != nil {
				t.Fatalf("forced: %v", err)
			}
			assertState(t, root, restored)
			if _, err := os.Stat(m); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("marker still there: %v", err)
			}
			assertNoLeftovers(t, root)
		})
	}
}

func TestRestoreNewBox(t *testing.T) {
	t.Parallel()
	code, locked := lockedKey(t)
	for name, o := range map[string]RestoreOptions{
		"asked for":          {NewBox: true},
		"the TPM refuses it": {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := stoppedBox(t)
			sys := &fakeSystem{decryptErr: errors.New("tpm: no such key")}
			asks := 0
			o.ReadCode = func() (string, error) { asks++; return code, nil }
			o.sleep = noSleep(new(int))
			rep, err := Restore(sys, root, backupFile(t, locked), o)
			if err != nil {
				t.Fatal(err)
			}
			if rep.SameBox || asks != 1 {
				t.Fatalf("report %+v, code asked %d times", rep, asks)
			}
			enc := "Encrypt " + staged(root, credentialFile)
			if !slices.Contains(sys.calls, enc) || !bytes.Equal(sys.lastEncrypted, masterKey()) {
				t.Fatalf("calls %q, encrypted %x; want %q with the unlocked key", sys.calls, sys.lastEncrypted, enc)
			}
			dec := slices.Contains(sys.calls, "Decrypt "+staged(root, credentialFile))
			if dec == o.NewBox {
				t.Fatalf("decrypted the staged credential: %v, new box %v", dec, o.NewBox)
			}
			if i, j := slices.Index(sys.calls, enc), slices.Index(sys.calls, "Stop "+stops()); i < 0 || j < i {
				t.Fatalf("calls = %q: sealed after stopping", sys.calls)
			}
			// The fake seals nothing: what was staged goes in.
			assertState(t, root, restored)
			assertNoLeftovers(t, root)
		})
	}
}

func TestRestoreWrongCodeChangesNothing(t *testing.T) {
	t.Parallel()
	_, locked := lockedKey(t)
	root := stoppedBox(t)
	sys := &fakeSystem{}
	_, err := Restore(sys, root, backupFile(t, locked), RestoreOptions{
		NewBox:   true,
		ReadCode: func() (string, error) { return otherCode, nil },
		sleep:    noSleep(new(int)),
	})
	if !errors.Is(err, ErrWrongCode) {
		t.Fatalf("err = %v, want ErrWrongCode", err)
	}
	if !reflect.DeepEqual(sys.calls, []string{"Calls"}) || sys.lastEncrypted != nil {
		t.Fatalf("calls = %q, encrypted %v", sys.calls, sys.lastEncrypted != nil)
	}
	assertState(t, root, untouched)
	if got := readFile(t, filepath.Join(root, Marker)); got != header().ID {
		t.Fatalf("marker = %q", got)
	}
	assertNoLeftovers(t, root)
}

func TestRestoreNoRecoveryCopyOnNewBox(t *testing.T) {
	for name, c := range map[string]struct {
		o       RestoreOptions
		sys     *fakeSystem
		decrypt bool
		cause   string // what the error must also say
	}{
		"asked for": {RestoreOptions{NewBox: true}, &fakeSystem{key: masterKey()}, false, ""},
		// Not asked for, but this box's TPM does not open the backup's key: the owner sees why.
		"the TPM refuses it": {RestoreOptions{}, &fakeSystem{decryptErr: errors.New("tpm: no such key")}, true, "tpm: no such key"},
	} {
		t.Run(name, func(t *testing.T) {
			root := stoppedBox(t)
			var asked bool
			c.o.ReadCode = refuseCode(&asked)
			_, err := Restore(c.sys, root, backupFile(t, nil), c.o)
			if err == nil || !strings.Contains(err.Error(), "restores on its own box only") || !strings.Contains(err.Error(), c.cause) {
				t.Fatalf("err = %v", err)
			}
			want := []string{"Calls"}
			if c.decrypt {
				want = append(want, "Decrypt "+staged(root, credentialFile))
			}
			if asked || !reflect.DeepEqual(c.sys.calls, want) {
				t.Fatalf("code asked %v, calls %q, want %q", asked, c.sys.calls, want)
			}
			assertState(t, root, untouched)
			assertNoLeftovers(t, root)
		})
	}
}

func TestRestoreRechecksBusyBeforeStopping(t *testing.T) {
	root := stoppedBox(t)
	// Idle when the restore begins; a call has come in by the time it would stop the services.
	sys := &fakeSystem{key: masterKey(), callsSeq: []string{"", busyCall}}
	_, err := Restore(sys, root, backupFile(t, nil), RestoreOptions{sleep: noSleep(new(int))})
	if err == nil || !strings.Contains(err.Error(), "a call is in progress") {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"Calls", "Decrypt " + staged(root, credentialFile), "Calls"}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("calls = %q, want %q", sys.calls, want)
	}
	assertState(t, root, untouched)
	assertNoLeftovers(t, root)
}

func TestRestoreSaysWhatFailedAfterTheSwap(t *testing.T) {
	root := stoppedBox(t)
	sys := &fakeSystem{key: masterKey(), startErr: errors.New("start refused")}
	_, err := Restore(sys, root, backupFile(t, nil), RestoreOptions{sleep: noSleep(new(int))})
	if err == nil || !strings.Contains(err.Error(), "restored, but") || !strings.Contains(err.Error(), "start refused") {
		t.Fatalf("err = %v", err)
	}
	assertState(t, root, restored)
}

func TestRestoreCarriesOnWithAnOldCopyLeft(t *testing.T) {
	root := stoppedBox(t)
	// The live state holds a directory whose file can't be deleted: its .old- copy stays.
	put(t, root, "var/lib/concierge/sub/f", "live", 0o600)
	sub := filepath.Join(root, "var/lib/concierge/sub")
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "var/lib/concierge.old-"+testID)
	t.Cleanup(func() { os.Chmod(sub, 0o755); os.Chmod(filepath.Join(old, "sub"), 0o755) })

	sys := &fakeSystem{key: masterKey()}
	rep, err := Restore(sys, root, backupFile(t, nil), RestoreOptions{sleep: noSleep(new(int))})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Leftovers, []string{old}) {
		t.Fatalf("leftovers = %q, want %q", rep.Leftovers, old)
	}
	assertState(t, root, restored)
	if !slices.Contains(sys.calls, "Start "+starts()) {
		t.Fatalf("calls = %q: not started", sys.calls)
	}
	if _, err := os.Stat(filepath.Join(root, Marker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker still there: %v", err)
	}
}

func TestRestoreRemovesStagingWhenExtractFails(t *testing.T) {
	root := stoppedBox(t)
	b := backupFile(t, nil)
	raw, _ := os.ReadFile(b)
	raw[len(raw)-8] ^= 0xff // the gzip trailer's CRC: Extract fails after writing everything
	os.WriteFile(b, raw, 0o600)
	sys := &fakeSystem{key: masterKey()}
	if _, err := Restore(sys, root, b, RestoreOptions{}); err == nil {
		t.Fatal("corrupt backup restored")
	}
	if !reflect.DeepEqual(sys.calls, []string{"Calls"}) {
		t.Fatalf("calls = %q", sys.calls)
	}
	assertState(t, root, untouched)
	assertNoLeftovers(t, root)
}

func TestRestoreSwapsOnlyTheBackupsPaths(t *testing.T) {
	root := stoppedBox(t)
	put(t, root, "var/lib/concierge-telegram/telegram.db", "live", 0o600)
	// A hand-made backup whose link steers an entry to staging's var/lib/concierge-telegram.
	h := header()
	h.Paths = []string{"var/lib/concierge"}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Fatal(err)
	}
	entry := func(name string, typ byte, link string) tar.Header {
		return tar.Header{Name: name, Typeflag: typ, Linkname: link, Uname: u.Username, Gname: g.Name}
	}
	b := archiveOf(t, h,
		entry("var/lib/concierge/", tar.TypeDir, ""),
		entry("var/lib/concierge/signins.db", tar.TypeReg, ""),
		entry("var/lib/concierge/link", tar.TypeSymlink, ".."),
		entry("var/lib/concierge/link/concierge-telegram/telegram.db", tar.TypeReg, ""),
	)
	f := filepath.Join(t.TempDir(), "b.tar.gz")
	os.WriteFile(f, b.Bytes(), 0o600)
	if _, err := Restore(&fakeSystem{key: masterKey()}, root, f, RestoreOptions{Force: true, sleep: noSleep(new(int))}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "var/lib/concierge/signins.db")); got != "x" {
		t.Fatalf("signins.db = %q", got)
	}
	if got := readFile(t, filepath.Join(root, "var/lib/concierge-telegram/telegram.db")); got != "live" {
		t.Fatalf("a path outside the backup's was swapped: %q", got)
	}
	assertNoLeftovers(t, root)
}

func TestSwapRollsBackOnFailure(t *testing.T) {
	root, staging := t.TempDir(), t.TempDir()
	paths := []string{"a/one", "b/two", "c/three"}
	for _, p := range paths {
		put(t, root, p+"/f", "live", 0o600)
		put(t, staging, p+"/f", "staged", 0o600)
	}
	// The third live path cannot be moved aside.
	ro := filepath.Join(root, "c")
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })

	_, err := Swap(root, staging, testID, paths)
	if err == nil || !strings.Contains(err.Error(), "undone") || strings.Contains(err.Error(), "by hand") {
		t.Fatalf("err = %v, want it to say the swap was undone", err)
	}
	for _, p := range paths {
		if got := readFile(t, filepath.Join(root, p, "f")); got != "live" {
			t.Fatalf("%s = %q, want it back as it was", p, got)
		}
		if es, _ := filepath.Glob(filepath.Join(root, p+".old-*")); len(es) != 0 {
			t.Fatalf("old copy left: %v", es)
		}
	}
	// What was staged went back to staging: once the parent can be written, the swap goes through.
	if err := os.Chmod(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	if left, err := Swap(root, staging, testID, paths); err != nil || left != nil {
		t.Fatal(left, err)
	}
	for _, p := range paths {
		if got := readFile(t, filepath.Join(root, p, "f")); got != "staged" {
			t.Fatalf("%s = %q after the second swap", p, got)
		}
		if _, err := os.Stat(filepath.Join(root, p+".old-"+testID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s's old copy left: %v", p, err)
		}
	}
}

func TestSwapSaysWhenUndoingFails(t *testing.T) {
	root, staging := t.TempDir(), t.TempDir()
	paths := []string{"a/one", "b/two"}
	for _, p := range paths {
		put(t, root, p+"/f", "live", 0o600)
		put(t, staging, p+"/f", "staged", 0o600)
	}
	// b/two can't be put in place, and a/one can't be taken back out.
	rename := func(from, to string) error {
		if from == filepath.Join(staging, "b/two") || to == filepath.Join(staging, "a/one") {
			return errors.New("refused")
		}
		return os.Rename(from, to)
	}
	_, err := swap(rename, root, staging, testID, paths)
	old := filepath.Join(root, "a/one.old-"+testID)
	if err == nil || !strings.Contains(err.Error(), "by hand") || !strings.Contains(err.Error(), old) {
		t.Fatalf("err = %v, want it to name %s", err, old)
	}
	if got := readFile(t, filepath.Join(old, "f")); got != "live" {
		t.Fatalf("the old state's copy = %q", got)
	}
	if got := readFile(t, filepath.Join(root, "b/two/f")); got != "live" {
		t.Fatalf("b/two = %q, want it back", got)
	}
}

func TestSwapKeepsGoingWhenAnOldCopyStays(t *testing.T) {
	root, staging := t.TempDir(), t.TempDir()
	put(t, root, "a/one/sub/f", "live", 0o600)
	put(t, staging, "a/one/f", "staged", 0o600)
	if err := os.Chmod(filepath.Join(root, "a/one/sub"), 0o555); err != nil { // its file can't be deleted
		t.Fatal(err)
	}
	old := filepath.Join(root, "a/one.old-"+testID)
	t.Cleanup(func() { os.Chmod(filepath.Join(old, "sub"), 0o755) })

	left, err := Swap(root, staging, testID, []string{"a/one"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, []string{old}) {
		t.Fatalf("left = %q, want %q", left, old)
	}
	if got := readFile(t, filepath.Join(root, "a/one/f")); got != "staged" {
		t.Fatalf("a/one = %q", got)
	}
}

func TestSwapRefusesOldCopiesLeft(t *testing.T) {
	root, staging := t.TempDir(), t.TempDir()
	put(t, root, "a/one/f", "live", 0o600)
	put(t, root, "a/one.old-"+testID+"/f", "earlier", 0o600)
	put(t, staging, "a/one/f", "staged", 0o600)
	_, err := Swap(root, staging, testID, []string{"a/one"})
	if err == nil || !strings.Contains(err.Error(), "a/one.old-"+testID) {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, filepath.Join(root, "a/one/f")); got != "live" {
		t.Fatalf("a/one = %q", got)
	}
	if got := readFile(t, filepath.Join(staging, "a/one/f")); got != "staged" {
		t.Fatalf("staged a/one = %q", got)
	}
}

func TestSwapInstallsWhereNothingWas(t *testing.T) {
	root, staging := t.TempDir(), t.TempDir()
	put(t, staging, credentialFile, "sealed", 0o600)
	put(t, staging, "var/lib/concierge/signins.db", "staged", 0o600)
	put(t, root, "var/lib/concierge/signins.db", "live", 0o600)
	if left, err := Swap(root, staging, testID, []string{credentialFile, "var/lib/concierge"}); err != nil || left != nil {
		t.Fatal(left, err)
	}
	if got := readFile(t, filepath.Join(root, credentialFile)); got != "sealed" {
		t.Fatalf("credential = %q", got)
	}
	if fi, err := os.Stat(filepath.Join(root, "etc/credstore.encrypted")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("credstore: %v, %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(root, "etc")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("etc: %v, %v", fi, err)
	}
	if got := readFile(t, filepath.Join(root, "var/lib/concierge/signins.db")); got != "staged" {
		t.Fatalf("signins.db = %q", got)
	}
	assertNoLeftovers(t, root)
}

type distFile struct {
	body string
	mode int64
}

// distOf writes a dist archive (as scripts/dist.sh makes it) of files and returns its path.
func distOf(t *testing.T, files map[string]distFile) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, d := range []string{"bin/", "ui/", "browser/"} {
		tw.WriteHeader(&tar.Header{Name: d, Typeflag: tar.TypeDir, Mode: 0o755})
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		f := files[n]
		tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: f.mode, Size: int64(len(f.body))})
		tw.Write([]byte(f.body))
	}
	tw.Close()
	zw.Close()
	p := filepath.Join(t.TempDir(), "concierge.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// deployed is a box whose /opt/concierge holds a newer build than oldBuild.
func deployed(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range map[string]string{
		"bin/engine": "new engine", "bin/hostd": "new hostd", "bin/backup": "the box's backup",
		"ui/index.html": "new ui", "ui/new.js": "new", "browser/pyproject.toml": "new",
		"VERSION": "0.2.4\n", "REVISION": "def5678\n", "venv/bin/python": "venv",
	} {
		put(t, root, "opt/concierge/"+p, body, 0o755)
	}
	return root
}

// oldBuild is an older build's dist archive, from before the backup command.
func oldBuild() map[string]distFile {
	return map[string]distFile{
		"bin/engine":             {"old engine", 0o755},
		"bin/hostd":              {"old hostd", 0o755},
		"ui/index.html":          {"old ui", 0o644},
		"browser/pyproject.toml": {"old", 0o644},
		"browser/run.sh":         {"#!/bin/sh", 0o755},
		"VERSION":                {"0.2.3\n", 0o644},
		"REVISION":               {"abc1234\n", 0o644},
	}
}

func TestInstallBuildRefusesOtherDaemons(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(map[string]distFile)
		name string
	}{
		"an extra daemon":  {func(m map[string]distFile) { m["bin/sensord"] = distFile{"x", 0o755} }, "sensord"},
		"a missing daemon": {func(m map[string]distFile) { delete(m, "bin/hostd") }, "hostd"},
	} {
		t.Run(name, func(t *testing.T) {
			root := deployed(t)
			files := oldBuild()
			c.edit(files)
			sys := &fakeSystem{}
			_, err := InstallBuild(sys, root, distOf(t, files))
			if err == nil || !strings.Contains(err.Error(), c.name) {
				t.Fatalf("err = %v, want it to name %s", err, c.name)
			}
			if !reflect.DeepEqual(sys.calls, []string{"Calls"}) {
				t.Fatalf("calls = %q", sys.calls)
			}
			if got := readFile(t, filepath.Join(root, "opt/concierge/bin/engine")); got != "new engine" {
				t.Fatalf("engine = %q", got)
			}
			if es, _ := os.ReadDir(filepath.Join(root, "var/lib/concierge-backup")); len(es) != 0 {
				t.Fatalf("staging left: %v", es)
			}
		})
	}
}

func TestInstallBuild(t *testing.T) {
	root := deployed(t)
	sys := &fakeSystem{}
	if left, err := InstallBuild(sys, root, distOf(t, oldBuild())); err != nil || left != nil {
		t.Fatal(left, err)
	}
	opt := filepath.Join(root, "opt/concierge")
	if want := []string{"Calls", "Calls", "Stop " + stops(), "SyncVenv " + filepath.Join(opt, "browser")}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("calls = %q, want %q", sys.calls, want)
	}
	for p, f := range oldBuild() {
		path := filepath.Join(opt, p)
		if got := readFile(t, path); got != f.body {
			t.Fatalf("%s = %q, want %q", p, got, f.body)
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != fs.FileMode(f.mode) {
			t.Fatalf("%s mode %v, want %v", p, fi.Mode().Perm(), fs.FileMode(f.mode))
		}
	}
	for _, gone := range []string{"ui/new.js"} {
		if _, err := os.Stat(filepath.Join(opt, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s kept from the box's build: %v", gone, err)
		}
	}
	// The box keeps its own backup command (the restore that follows is its) and its venv.
	if got := readFile(t, filepath.Join(opt, "bin/backup")); got != "the box's backup" {
		t.Fatalf("bin/backup = %q", got)
	}
	if got := readFile(t, filepath.Join(opt, "venv/bin/python")); got != "venv" {
		t.Fatalf("venv = %q", got)
	}
	if es, _ := filepath.Glob(filepath.Join(opt, "*.old-*")); len(es) != 0 {
		t.Fatalf("old copies left: %v", es)
	}
	if es, _ := os.ReadDir(filepath.Join(root, "var/lib/concierge-backup")); len(es) != 0 {
		t.Fatalf("staging left: %v", es)
	}
}

func TestInstallBuildRefusesEntriesOutsideTheBuild(t *testing.T) {
	root := deployed(t)
	files := oldBuild()
	files["etc/passwd"] = distFile{"x", 0o644}
	sys := &fakeSystem{}
	if _, err := InstallBuild(sys, root, distOf(t, files)); err == nil {
		t.Fatal("no error")
	}
	if !reflect.DeepEqual(sys.calls, []string{"Calls"}) {
		t.Fatalf("calls = %q", sys.calls)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/passwd")); err == nil {
		t.Fatal("wrote outside the build")
	}
}

func TestInstallBuildRechecksBusyBeforeStopping(t *testing.T) {
	root := deployed(t)
	sys := &fakeSystem{callsSeq: []string{"", busyCall}}
	_, err := InstallBuild(sys, root, distOf(t, oldBuild()))
	if err == nil || !strings.Contains(err.Error(), "a call is in progress") {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"Calls", "Calls"}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("calls = %q, want %q", sys.calls, want)
	}
	if got := readFile(t, filepath.Join(root, "opt/concierge/bin/engine")); got != "new engine" {
		t.Fatalf("engine = %q", got)
	}
	if es, _ := os.ReadDir(filepath.Join(root, "var/lib/concierge-backup")); len(es) != 0 {
		t.Fatalf("staging left: %v", es)
	}
}

func TestInstallBuildSaysWhatFailedAfterTheSwap(t *testing.T) {
	root := deployed(t)
	sys := &fakeSystem{syncErr: errors.New("uv refused")}
	_, err := InstallBuild(sys, root, distOf(t, oldBuild()))
	if err == nil || !strings.Contains(err.Error(), "installed, but") || !strings.Contains(err.Error(), "uv refused") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, filepath.Join(root, "opt/concierge/bin/engine")); got != "old engine" {
		t.Fatalf("engine = %q", got)
	}
}
