package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

// shortDir is a temp dir with a short path: a Unix socket's path is capped (104 bytes on macOS).
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "bk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func put(t *testing.T, root, rel, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

type fakeOwners struct {
	mu      sync.Mutex
	lookups [][2]string
	chowns  map[string][2]int
}

func (f *fakeOwners) Lookup(user, group string) (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, [2]string{user, group})
	return 1234, 5678, nil
}

func (f *fakeOwners) Chown(path string, uid, gid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.chowns == nil {
		f.chowns = map[string][2]int{}
	}
	f.chowns[path] = [2]int{uid, gid}
	return nil
}

func header() Header {
	return Header{
		Format: 1, ID: "0123456789abcdef0123456789abcdef", Version: "0.2.3", Revision: "abc1234",
		Box: "box", TakenAt: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), Recovery: true,
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	root := shortDir(t)
	put(t, root, "var/lib/concierge/a.db", "alpha", 0o600)
	put(t, root, "etc/concierge/engine.env", "beta", 0o640)
	put(t, root, "etc/credstore.encrypted/concierge-secrets-master", "gamma", 0o600)
	put(t, root, "var/lib/concierge/sub/deep.txt", "delta", 0o640)
	if err := os.Chmod(filepath.Join(root, "var/lib/concierge/sub"), 0o750); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Write(&buf, header(), root); err != nil {
		t.Fatal(err)
	}
	staging := shortDir(t)
	got, err := Extract(&buf, staging, &fakeOwners{})
	if err != nil {
		t.Fatal(err)
	}

	want := header()
	want.Paths = []string{"var/lib/concierge", "etc/concierge", "etc/credstore.encrypted/concierge-secrets-master"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("header = %+v, want %+v", got, want)
	}
	for rel, c := range map[string]struct {
		body string
		mode os.FileMode
	}{
		"var/lib/concierge/a.db":                           {"alpha", 0o600},
		"etc/concierge/engine.env":                         {"beta", 0o640},
		"etc/credstore.encrypted/concierge-secrets-master": {"gamma", 0o600},
		"var/lib/concierge/sub/deep.txt":                   {"delta", 0o640},
	} {
		p := filepath.Join(staging, rel)
		b, err := os.ReadFile(p)
		if err != nil || string(b) != c.body {
			t.Fatalf("%s: %q, %v", rel, b, err)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != c.mode {
			t.Fatalf("%s mode = %v, want %v", rel, fi.Mode().Perm(), c.mode)
		}
	}
	if fi, err := os.Stat(filepath.Join(staging, "var/lib/concierge/sub")); err != nil || fi.Mode().Perm() != 0o750 {
		t.Fatalf("dir mode: %v, %v", fi, err)
	}
}

func TestArchiveOwnersByName(t *testing.T) {
	root := shortDir(t)
	put(t, root, "var/lib/concierge/a.db", "alpha", 0o600)

	var buf bytes.Buffer
	if err := Write(&buf, header(), root); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	zr, _ := gzip.NewReader(bytes.NewReader(raw))
	tr := tar.NewReader(zr)
	names := map[string][2]string{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names[h.Name] = [2]string{h.Uname, h.Gname}
	}
	if n := names["var/lib/concierge/a.db"]; n[0] == "" || n[1] == "" {
		t.Fatalf("entry carries no names: %v", names)
	}

	o := &fakeOwners{}
	staging := shortDir(t)
	if _, err := Extract(bytes.NewReader(raw), staging, o); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"var/lib/concierge", "var/lib/concierge/a.db"} {
		if c := o.chowns[filepath.Join(staging, rel)]; c != [2]int{1234, 5678} {
			t.Fatalf("chown %s = %v", rel, c)
		}
	}
	want := names["var/lib/concierge/a.db"]
	found := false
	for _, l := range o.lookups {
		if l == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("lookups %v lack %v", o.lookups, want)
	}
}

func TestArchiveSkipsSocketsKeepsSymlinks(t *testing.T) {
	root := shortDir(t)
	prof := filepath.Join(root, "var/lib/concierge-worker/profiles/site")
	if err := os.MkdirAll(prof, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("host-123", filepath.Join(prof, "SingletonLock")); err != nil {
		t.Fatal(err)
	}
	// Bound under a short path (the root's is too long for a socket), then moved into the profile.
	short := shortDir(t)
	l, err := net.Listen("unix", filepath.Join(short, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := os.Rename(filepath.Join(short, "s"), filepath.Join(prof, "s.sock")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Write(&buf, header(), root); err != nil {
		t.Fatal(err)
	}
	staging := shortDir(t)
	if _, err := Extract(&buf, staging, &fakeOwners{}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(staging, "var/lib/concierge-worker/profiles/site")
	if tgt, err := os.Readlink(filepath.Join(out, "SingletonLock")); err != nil || tgt != "host-123" {
		t.Fatalf("symlink = %q, %v", tgt, err)
	}
	if _, err := os.Lstat(filepath.Join(out, "s.sock")); !os.IsNotExist(err) {
		t.Fatalf("socket came back: %v", err)
	}
}

func tarOf(t *testing.T, names ...string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	hb, _ := json.Marshal(header())
	tw.WriteHeader(&tar.Header{Name: "backup.json", Mode: 0o600, Size: int64(len(hb))})
	tw.Write(hb)
	for _, n := range names {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o600, Size: 1})
		tw.Write([]byte("x"))
	}
	tw.Close()
	zw.Close()
	return &buf
}

func TestExtractRefusesEscapes(t *testing.T) {
	for _, name := range []string{"../x", "var/../../x", "/etc/x"} {
		outer := shortDir(t)
		staging := filepath.Join(outer, "stage")
		if err := os.Mkdir(staging, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Extract(tarOf(t, name), staging, &fakeOwners{}); err == nil {
			t.Fatalf("%q: no error", name)
		}
		if _, err := os.Stat(filepath.Join(outer, "x")); err == nil {
			t.Fatalf("%q: wrote outside staging", name)
		}
		if _, err := os.Stat("/etc/x"); err == nil {
			t.Fatalf("%q: wrote /etc/x", name)
		}
	}
}

func TestExtractRefusesOtherFormats(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	hb, _ := json.Marshal(Header{Format: 2})
	tw.WriteHeader(&tar.Header{Name: "backup.json", Mode: 0o600, Size: int64(len(hb))})
	tw.Write(hb)
	tw.Close()
	zw.Close()
	if _, err := Extract(&buf, shortDir(t), &fakeOwners{}); err == nil {
		t.Fatal("format 2 accepted")
	}
}

func TestReadHeaderFirstEntry(t *testing.T) {
	root := shortDir(t)
	big := make([]byte, 1<<20)
	rand.Read(big)
	put(t, root, "var/lib/concierge/big.bin", string(big), 0o600)
	var buf bytes.Buffer
	if err := Write(&buf, header(), root); err != nil {
		t.Fatal(err)
	}
	// Only the first 16 KiB, then a read error: the header is all ReadHeader needs.
	r := io.MultiReader(bytes.NewReader(buf.Bytes()[:16<<10]), iotest.ErrReader(errors.New("cut")))
	h, err := ReadHeader(r)
	if err != nil {
		t.Fatal(err)
	}
	if h.ID != header().ID || !reflect.DeepEqual(h.Paths, []string{"var/lib/concierge"}) {
		t.Fatalf("header = %+v", h)
	}
}
