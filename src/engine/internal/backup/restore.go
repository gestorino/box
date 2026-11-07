package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// RestoreOptions are a restore's choices. Force restores a backup the box was not stopped for;
// NewBox ignores the box's own TPM credential (the other-box path, tried on the same box);
// ReadCode asks the owner for the recovery code, only when the other-box path needs it.
type RestoreOptions struct {
	Force, NewBox bool
	ReadCode      func() (string, error)

	sleep func(time.Duration) // the services' poll; time.Sleep when nil
}

// Report is what a restore did: the backup it put back, on its own box or another.
type Report struct {
	SameBox bool
	Header  Header
}

// startWait is how long a restore waits for every service to come back.
const startWait = 60 * time.Second

// Restore puts the backup at from back on the box under root, replacing the state the backup
// holds: it extracts to a staging directory, opens the master key (the box's TPM, else the
// recovery code, then sealed to this TPM), stops the services, swaps the backup's paths in and
// starts the services again. Until the swap nothing on the box changes; the staging directory is
// gone afterwards, whatever happened. A service that is not running a minute later is an error
// that names it, though the state stays restored.
func Restore(sys System, root, from string, o RestoreOptions) (Report, error) {
	if err := idle(sys, root); err != nil {
		return Report{}, err
	}
	f, err := os.Open(from)
	if err != nil {
		return Report{}, err
	}
	defer f.Close()
	h, err := ReadHeader(f)
	if err != nil {
		return Report{}, err
	}
	if b, err := hex.DecodeString(h.ID); err != nil || len(b) != 16 {
		return Report{}, fmt.Errorf("the backup's ID %q is not one a backup is given", h.ID)
	}
	version, revision, err := build(root)
	if err != nil {
		return Report{}, err
	}
	if version != h.Version || revision != h.Revision {
		return Report{}, fmt.Errorf("the backup was taken on %s (%s) and the box runs %s (%s): install the backup's build first",
			h.Version, h.Revision, version, revision)
	}
	if !o.Force {
		stoppedFor, err := readMarker(root)
		if err != nil {
			return Report{}, err
		}
		if stoppedFor != h.ID {
			return Report{}, errors.New("the box was not stopped for this backup (make backup STOP=1): what arrived since it was taken would be lost, and WhatsApp may need linking again; FORCE=1 restores it anyway")
		}
	}

	dir, err := stateDir(root)
	if err != nil {
		return Report{}, err
	}
	staging := filepath.Join(dir, "staging-"+h.ID)
	if err := os.RemoveAll(staging); err != nil { // an interrupted restore's
		return Report{}, err
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(staging)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Report{}, err
	}
	staged, err := Extract(f, staging, SystemOwners{})
	if err != nil {
		return Report{}, fmt.Errorf("extracting the backup: %w", err)
	}
	if staged.ID != h.ID {
		return Report{}, errors.New("the backup changed while it was read")
	}

	same, err := openMasterKey(sys, filepath.Join(staging, credentialFile), filepath.Join(staging, RecoveryFile), o)
	if err != nil {
		return Report{}, err
	}

	if err := sys.Stop(Units); err != nil {
		return Report{}, err
	}
	if err := Swap(root, staging, staged.Paths); err != nil {
		return Report{}, fmt.Errorf("putting the backup in place, undone: %w (the services are stopped: make backup-resume starts them)", err)
	}
	if err := sys.Unmask(Units); err != nil {
		return Report{}, err
	}
	if err := sys.Start(reversed(Units)); err != nil {
		return Report{}, err
	}
	if err := removeMarker(root); err != nil {
		return Report{}, err
	}
	if err := os.RemoveAll(staging); err != nil {
		return Report{}, err
	}
	rep := Report{SameBox: same, Header: staged}
	sleep := o.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	return rep, waitActive(sys, sleep)
}

// openMasterKey leaves the staged credential one this box's TPM opens and says whether it already
// was (the same box). Otherwise the recovery code unlocks the staged recovery copy and the key is
// sealed over the staged credential. The key stays in memory and pipes.
func openMasterKey(sys System, cred, recovery string, o RestoreOptions) (sameBox bool, err error) {
	if !o.NewBox {
		if key, err := sys.Decrypt(cred); err == nil {
			clear(key)
			return true, nil
		}
	}
	locked, err := os.ReadFile(recovery)
	if errors.Is(err, fs.ErrNotExist) {
		return false, errors.New("this backup has no recovery copy: it restores on its own box only")
	}
	if err != nil {
		return false, err
	}
	if o.ReadCode == nil {
		return false, errors.New("the recovery code is needed and there is no way to ask for it")
	}
	code, err := o.ReadCode()
	if err != nil {
		return false, fmt.Errorf("reading the recovery code: %w", err)
	}
	key, err := Unlock(locked, code)
	if err != nil {
		return false, fmt.Errorf("%w: nothing changed", err)
	}
	defer clear(key)
	if err := sys.Encrypt(key, cred); err != nil {
		return false, fmt.Errorf("sealing the master key to this box: %w", err)
	}
	return false, nil
}

// waitActive polls every service in Units each second until all are active, for startWait at
// most, then names those that are not.
func waitActive(sys System, sleep func(time.Duration)) error {
	for waited := time.Duration(0); ; waited += time.Second {
		var down []string
		for _, u := range Units {
			if !strings.HasSuffix(u, ".service") {
				continue
			}
			up, err := sys.Active(u)
			if err != nil {
				return err
			}
			if !up {
				down = append(down, u)
			}
		}
		if len(down) == 0 {
			return nil
		}
		if waited >= startWait {
			return fmt.Errorf("the backup is restored, but %s did not start within %v (journalctl -u <unit> says why)",
				strings.Join(down, ", "), startWait)
		}
		sleep(time.Second)
	}
}

// parentModes are the modes of the parents Swap may have to make, by their path; any other is 0755.
var parentModes = map[string]fs.FileMode{"etc/credstore.encrypted": 0o700}

// Swap puts each of paths (relative, a directory or a file) from staging in place under root: the
// live one, if any, is renamed aside to <path>.old-<id>, the staged one renamed in. A failure puts
// back every path already done; success removes the copies aside. Staging and root must be on one
// filesystem.
func Swap(root, staging string, paths []string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	type move struct {
		path  string
		aside bool // a live one was moved aside
	}
	var done []move
	undo := func(err error) error {
		errs := []error{err}
		for _, m := range slices.Backward(done) {
			live := filepath.Join(root, m.path)
			errs = append(errs, os.Rename(live, filepath.Join(staging, m.path)))
			if m.aside {
				errs = append(errs, os.Rename(live+".old-"+id, live))
			}
		}
		return errors.Join(errs...)
	}
	for _, p := range paths {
		live := filepath.Join(root, p)
		old := live + ".old-" + id
		if err := ensureParent(root, filepath.Dir(p)); err != nil {
			return undo(err)
		}
		aside, err := exists(live)
		if err != nil {
			return undo(err)
		}
		if aside {
			if err := os.Rename(live, old); err != nil {
				return undo(err)
			}
		}
		if err := os.Rename(filepath.Join(staging, p), live); err != nil {
			if aside {
				err = errors.Join(err, os.Rename(old, live))
			}
			return undo(err)
		}
		done = append(done, move{p, aside})
	}
	var errs []error
	for _, m := range done {
		if m.aside {
			errs = append(errs, os.RemoveAll(filepath.Join(root, m.path)+".old-"+id))
		}
	}
	return errors.Join(errs...)
}

// ensureParent makes dir (relative to root) and its missing parents, each with its parentModes mode.
func ensureParent(root, dir string) error {
	if dir == "." {
		return nil
	}
	path := filepath.Join(root, dir)
	if ok, err := exists(path); ok || err != nil {
		return err
	}
	if err := ensureParent(root, filepath.Dir(dir)); err != nil {
		return err
	}
	mode, ok := parentModes[filepath.ToSlash(dir)]
	if !ok {
		mode = 0o755
	}
	if err := os.Mkdir(path, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// buildPaths are what a dist archive holds, as /opt/concierge holds them.
var buildPaths = []string{"bin", "ui", "browser", "VERSION", "REVISION"}

// InstallBuild installs the build in a dist archive (scripts/dist.sh's) in place of the box's: its
// daemons must be the box's (the build runs under the box's units), the box keeps its own backup
// command (the restore that follows is the box's), the services are stopped and the worker's venv
// rebuilt. It starts nothing: the restore that follows does. Run as root, every file it writes is
// root's, with the archive's modes.
func InstallBuild(sys System, root, archive string) error {
	if err := idle(sys, root); err != nil {
		return err
	}
	dir, err := stateDir(root)
	if err != nil {
		return err
	}
	staging, err := os.MkdirTemp(dir, "build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := extractBuild(archive, staging); err != nil {
		return fmt.Errorf("the build's archive: %w", err)
	}
	for _, p := range buildPaths {
		if ok, err := exists(filepath.Join(staging, p)); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("the build's archive has no %s", p)
		}
	}
	opt := filepath.Join(root, "opt/concierge")
	if err := sameDaemons(filepath.Join(staging, "bin"), filepath.Join(opt, "bin")); err != nil {
		return err
	}
	if err := keepOwnBackup(filepath.Join(opt, "bin/backup"), filepath.Join(staging, "bin/backup")); err != nil {
		return err
	}
	if err := sys.Stop(Units); err != nil {
		return err
	}
	if err := Swap(opt, staging, buildPaths); err != nil {
		return fmt.Errorf("installing the build, undone: %w (the services are stopped: make backup-resume starts them)", err)
	}
	return sys.SyncVenv(filepath.Join(opt, "browser"))
}

// extractBuild unpacks a dist archive's directories and files into staging, modes as stored,
// through an os.Root on it; anything else, or outside buildPaths, is refused.
func extractBuild(archive, staging string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	root, err := os.OpenRoot(staging)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(zr)
	for {
		th, err := tr.Next()
		if err == io.EOF {
			// Draining the gzip trailer checks its checksum.
			_, err := io.Copy(io.Discard, zr)
			return err
		}
		if err != nil {
			return err
		}
		if err := extractBuildEntry(tr, th, root); err != nil {
			return fmt.Errorf("%s: %w", th.Name, err)
		}
	}
}

func extractBuildEntry(tr *tar.Reader, th *tar.Header, root *os.Root) error {
	name := filepath.Clean(filepath.FromSlash(th.Name))
	first, _, _ := strings.Cut(filepath.ToSlash(name), "/")
	if strings.HasPrefix(th.Name, "/") || !filepath.IsLocal(name) ||
		!(slices.Contains(buildPaths, name) || slices.Contains([]string{"bin", "ui", "browser"}, first)) {
		return errors.New("not part of a build")
	}
	mode := fs.FileMode(th.Mode).Perm()
	switch th.Typeflag {
	case tar.TypeDir:
		if err := root.MkdirAll(name, 0o755); err != nil {
			return err
		}
	case tar.TypeReg:
		if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		w, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, tr)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported entry type %q", th.Typeflag)
	}
	return root.Chmod(name, mode)
}

// sameDaemons refuses a build whose daemons (bin/'s files but backup) are not the box's.
func sameDaemons(build, box string) error {
	b, err := daemons(build)
	if err != nil {
		return err
	}
	o, err := daemons(box)
	if err != nil {
		return err
	}
	var diff []string
	if only := without(b, o); len(only) > 0 {
		diff = append(diff, "only in the build: "+strings.Join(only, ", "))
	}
	if only := without(o, b); len(only) > 0 {
		diff = append(diff, "only on the box: "+strings.Join(only, ", "))
	}
	if diff != nil {
		return fmt.Errorf("the build's daemons are not the box's, and it would run under the box's units (%s)", strings.Join(diff, "; "))
	}
	return nil
}

func daemons(bin string) ([]string, error) {
	entries, err := os.ReadDir(bin)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && e.Name() != "backup" {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// without is a's names that b lacks.
func without(a, b []string) []string {
	return slices.DeleteFunc(slices.Clone(a), func(n string) bool { return slices.Contains(b, n) })
}

// keepOwnBackup puts the box's backup command (a hard link) in the staged bin/, over the build's.
func keepOwnBackup(own, staged string) error {
	if ok, err := exists(own); !ok || err != nil {
		return err
	}
	if err := os.Remove(staged); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Link(own, staged)
}
