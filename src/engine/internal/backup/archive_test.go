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
	"slices"
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
}

// Lookup answers with the test's own ids, which an unprivileged Lchown accepts.
func (f *fakeOwners) Lookup(user, group string) (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, [2]string{user, group})
	return os.Getuid(), os.Getgid(), nil
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
	// One lookup per entry (the directory and the file), by the names the entries carry.
	want := names["var/lib/concierge/a.db"]
	if len(o.lookups) != 2 || o.lookups[0] != names["var/lib/concierge/"] || o.lookups[1] != want {
		t.Fatalf("lookups %v, want the entries' names %v", o.lookups, want)
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

// archiveOf hand-makes an archive: backup.json for h, then the entries (a regular file's body is "x").
func archiveOf(t *testing.T, h Header, entries ...tar.Header) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	hb, _ := json.Marshal(h)
	tw.WriteHeader(&tar.Header{Name: "backup.json", Mode: 0o600, Size: int64(len(hb))})
	tw.Write(hb)
	for _, e := range entries {
		if e.Typeflag == 0 || e.Typeflag == tar.TypeReg {
			e.Typeflag, e.Size = tar.TypeReg, 1
		}
		e.Mode = 0o750
		tw.WriteHeader(&e)
		if e.Typeflag == tar.TypeReg {
			tw.Write([]byte("x"))
		}
	}
	tw.Close()
	zw.Close()
	return &buf
}

func withPaths(paths ...string) Header {
	h := header()
	h.Paths = paths
	return h
}

func tarOf(t *testing.T, names ...string) *bytes.Buffer {
	var es []tar.Header
	for _, n := range names {
		es = append(es, tar.Header{Name: n})
	}
	return archiveOf(t, withPaths("var/lib/concierge"), es...)
}

func TestExtractRefusesSymlinkEscapes(t *testing.T) {
	for name, entries := range map[string][]tar.Header{
		"file through an absolute link": {
			{Name: "var/lib/concierge/x", Typeflag: tar.TypeSymlink, Linkname: "@OUT@"},
			{Name: "var/lib/concierge/x/evil"},
		},
		"file through a relative link": {
			{Name: "var/lib/concierge/x", Typeflag: tar.TypeSymlink, Linkname: "../../../../out"},
			{Name: "var/lib/concierge/x/evil"},
		},
		"directory over a link": {
			{Name: "var/lib/concierge/x", Typeflag: tar.TypeSymlink, Linkname: "@OUT@"},
			{Name: "var/lib/concierge/x/", Typeflag: tar.TypeDir},
		},
	} {
		t.Run(name, func(t *testing.T) {
			outer := shortDir(t)
			// out sits beside staging's grandparent: four levels up from staging/var/lib/concierge.
			out := filepath.Join(outer, "a", "b", "out")
			staging := filepath.Join(outer, "a", "b", "stage")
			for _, d := range []string{out, staging} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for i := range entries {
				if entries[i].Linkname == "@OUT@" {
					entries[i].Linkname = out
				}
			}
			if _, err := Extract(archiveOf(t, withPaths("var/lib/concierge"), entries...), staging, &fakeOwners{}); err == nil {
				t.Fatal("no error")
			}
			if fis, _ := os.ReadDir(out); len(fis) != 0 {
				t.Fatalf("wrote outside staging: %v", fis)
			}
			if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o755 {
				t.Fatalf("outside directory's mode changed to %v", fi.Mode().Perm())
			}
		})
	}
}

func TestExtractRefusesBadChecksum(t *testing.T) {
	root := shortDir(t)
	put(t, root, "var/lib/concierge/a.db", "alpha", 0o600)
	var buf bytes.Buffer
	if err := Write(&buf, header(), root); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	raw[len(raw)-8] ^= 0xff // the gzip trailer's CRC
	if _, err := Extract(bytes.NewReader(raw), shortDir(t), &fakeOwners{}); err == nil {
		t.Fatal("corrupt trailer accepted")
	}
}

func TestExtractConfinesToBackupPaths(t *testing.T) {
	staging := shortDir(t)
	// A header naming a path a backup never holds.
	if _, err := Extract(archiveOf(t, withPaths("etc")), staging, &fakeOwners{}); err == nil {
		t.Fatal("header path outside Paths accepted")
	}
	// An entry outside the header's paths, though it is one a backup could hold.
	if _, err := Extract(archiveOf(t, withPaths("var/lib/concierge"), tar.Header{Name: "etc/concierge/x"}), staging, &fakeOwners{}); err == nil {
		t.Fatal("entry outside the header's paths accepted")
	}
	// A sibling that merely shares a prefix.
	if _, err := Extract(archiveOf(t, withPaths("var/lib/concierge"), tar.Header{Name: "var/lib/concierge-worker/x"}), staging, &fakeOwners{}); err == nil {
		t.Fatal("prefix sibling accepted")
	}
	if _, err := os.Stat(filepath.Join(staging, "etc")); err == nil {
		t.Fatal("wrote an entry outside the paths")
	}
}

// Each of the header's paths must come out as what a backup holds there: the master key's
// credential a regular file (systemd-creds, root and outside any os.Root, would follow a link),
// every other path a directory, each reached through real directories alone.
func TestExtractRefusesAPathOfTheWrongKind(t *testing.T) {
	link := func(name, to string) tar.Header {
		return tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: to}
	}
	dir := func(name string) tar.Header { return tar.Header{Name: name, Typeflag: tar.TypeDir} }
	for name, c := range map[string]struct {
		paths   []string
		entries []tar.Header
	}{
		"the credential a link":      {[]string{credentialFile}, []tar.Header{link(credentialFile, "/etc/shadow")}},
		"the credential a directory": {[]string{credentialFile}, []tar.Header{dir(credentialFile + "/")}},
		"something beneath the credential": {[]string{credentialFile}, []tar.Header{
			{Name: credentialFile + "/x"},
		}},
		"a directory path a link":         {[]string{"var/lib/concierge"}, []tar.Header{link("var/lib/concierge", "elsewhere")}},
		"a directory path a regular file": {[]string{"etc/concierge"}, []tar.Header{{Name: "etc/concierge"}}},
		"the credential a link made through another path": {[]string{"var/lib/concierge", credentialFile}, []tar.Header{
			dir("var/lib/concierge/"),
			link("var/lib/concierge/l", "../../../etc"),
			link("var/lib/concierge/l/credstore.encrypted/concierge-secrets-master", "/etc/shadow"),
		}},
		"a path's parent a link made through another path": {[]string{"var/lib/concierge", "etc/concierge"}, []tar.Header{
			dir("var/lib/concierge/"),
			dir("var/lib/concierge/real/"),
			link("var/lib/concierge/l", "../../.."),
			link("var/lib/concierge/l/etc", "var/lib/concierge/real"),
			dir("etc/concierge/"),
		}},
		"a path the header names but the archive lacks": {[]string{"var/lib/concierge", "etc/concierge"}, []tar.Header{
			dir("var/lib/concierge/"),
		}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Extract(archiveOf(t, withPaths(c.paths...), c.entries...), shortDir(t), &fakeOwners{})
			if err == nil {
				t.Fatal("no error")
			}
			t.Log(err)
		})
	}
	// The right kinds pass.
	ok := archiveOf(t, withPaths("var/lib/concierge", credentialFile), dir("var/lib/concierge/"), tar.Header{Name: credentialFile})
	if _, err := Extract(ok, shortDir(t), &fakeOwners{}); err != nil {
		t.Fatal(err)
	}
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

func TestPathsHoldRelaydsState(t *testing.T) {
	// relay.db (the box's identity and its devices) and certmagic's storage; the box secret and the
	// device CA are secretd's, backed up with it.
	if !slices.Contains(Paths, "var/lib/concierge-relay") {
		t.Fatalf("Paths %v leave out var/lib/concierge-relay", Paths)
	}
}
