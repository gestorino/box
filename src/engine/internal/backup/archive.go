package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// headerName is the archive's first entry.
const headerName = "backup.json"

// Header is backup.json: what a backup is and which of Paths it holds.
type Header struct {
	Format   int       `json:"format"`
	ID       string    `json:"id"`
	Version  string    `json:"version"`
	Revision string    `json:"revision"`
	Box      string    `json:"box"`
	TakenAt  time.Time `json:"taken_at"`
	Recovery bool      `json:"recovery"`
	Paths    []string  `json:"paths"`
}

// Paths are what a backup holds, relative to the root: directories, and the TPM-sealed master
// key's one file.
var Paths = []string{
	"var/lib/concierge",
	"var/lib/concierge-worker",
	"var/lib/concierge-hostd",
	"var/lib/concierge-secrets",
	"var/lib/concierge-sms",
	"var/lib/concierge-calls",
	"var/lib/concierge-whatsapp",
	"var/lib/concierge-telegram",
	"var/lib/concierge-email",
	"etc/concierge",
	"etc/credstore.encrypted/concierge-secrets-master",
}

// Write writes a gzip'd tar of those of Paths that exist under root, backup.json first (h with
// its Paths filled in). Symlinks are kept as links; sockets, devices and pipes are left out.
func Write(w io.Writer, h Header, root string) error {
	h.Paths = nil
	for _, p := range Paths {
		if _, err := os.Lstat(filepath.Join(root, p)); err == nil {
			h.Paths = append(h.Paths, p)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	zw, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	body, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: headerName, Mode: 0o600, Size: int64(len(body)), ModTime: h.TakenAt}); err != nil {
		return err
	}
	if _, err := tw.Write(body); err != nil {
		return err
	}
	for _, p := range h.Paths {
		err := filepath.WalkDir(filepath.Join(root, p), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return addEntry(tw, root, path, d)
		})
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

func addEntry(tw *tar.Writer, root, path string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	var link string
	switch m := info.Mode().Type(); {
	case m == 0, m == fs.ModeDir:
	case m == fs.ModeSymlink:
		if link, err = os.Readlink(path); err != nil {
			return err
		}
	default: // sockets, devices, pipes
		return nil
	}
	h, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	h.Name = filepath.ToSlash(rel)
	if info.IsDir() {
		h.Name += "/"
	}
	if h.Uname, h.Gname, err = names(h.Uid, h.Gid); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	if err := tw.WriteHeader(h); err != nil {
		return err
	}
	if h.Typeflag != tar.TypeReg {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func names(uid, gid int) (string, string, error) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return "", "", err
	}
	g, err := user.LookupGroupId(strconv.Itoa(gid))
	if err != nil {
		return "", "", err
	}
	return u.Username, g.Name, nil
}

// ReadHeader reads backup.json, the first entry, and nothing after it.
func ReadHeader(r io.Reader) (Header, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return Header{}, err
	}
	defer zr.Close()
	return readHeader(tar.NewReader(zr))
}

func readHeader(tr *tar.Reader) (Header, error) {
	th, err := tr.Next()
	if err != nil {
		return Header{}, fmt.Errorf("reading %s: %w", headerName, err)
	}
	if th.Name != headerName {
		return Header{}, fmt.Errorf("first entry is %q, not %s", th.Name, headerName)
	}
	var h Header
	if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&h); err != nil {
		return Header{}, fmt.Errorf("%s: %w", headerName, err)
	}
	return h, nil
}

// Owners maps an entry's owner names to this machine's ids and applies them.
type Owners interface {
	Lookup(user, group string) (uid, gid int, err error)
	Chown(path string, uid, gid int) error
}

// SystemOwners is Owners on the real machine: os/user lookups and Lchown (never following a link).
type SystemOwners struct{}

func (SystemOwners) Lookup(name, group string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

func (SystemOwners) Chown(path string, uid, gid int) error { return os.Lchown(path, uid, gid) }

// Extract unpacks an archive into staging/<path>, owners by name through o, modes as stored. It
// refuses an entry outside staging and a header whose Format is not 1.
func Extract(r io.Reader, staging string, o Owners) (Header, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return Header{}, err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	h, err := readHeader(tr)
	if err != nil {
		return Header{}, err
	}
	if h.Format != 1 {
		return Header{}, fmt.Errorf("backup format %d is not supported", h.Format)
	}
	for {
		th, err := tr.Next()
		if err == io.EOF {
			return h, nil
		}
		if err != nil {
			return Header{}, err
		}
		if err := extractEntry(tr, th, staging, o); err != nil {
			return Header{}, fmt.Errorf("%s: %w", th.Name, err)
		}
	}
}

func extractEntry(tr *tar.Reader, th *tar.Header, staging string, o Owners) error {
	name := filepath.FromSlash(th.Name)
	if filepath.IsAbs(name) || strings.HasPrefix(th.Name, "/") || !filepath.IsLocal(name) {
		return errors.New("outside the staging directory")
	}
	dest := filepath.Join(staging, name)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(th.Mode).Perm()
	switch th.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(dest, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dest, mode); err != nil {
			return err
		}
	case tar.TypeReg:
		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if err := os.Chmod(dest, mode); err != nil {
			return err
		}
	case tar.TypeSymlink:
		if err := os.Symlink(th.Linkname, dest); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported entry type %q", th.Typeflag)
	}
	uid, gid, err := o.Lookup(th.Uname, th.Gname)
	if err != nil {
		return err
	}
	return o.Chown(dest, uid, gid)
}
