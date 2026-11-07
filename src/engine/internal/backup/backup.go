package backup

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Marker holds the ID of the backup the box's services were stopped for, relative to the root.
// Every Concierge unit has ConditionPathExists=! on it, so while it exists none starts, reboot
// included; a restore of that backup, or a resume, removes it and starts them again.
const Marker = "var/lib/concierge-backup/stopped-for"

// backupDir holds the marker and a restore's staging directories, root's alone.
const backupDir = "var/lib/concierge-backup"

// credentialFile is secretd's master key, sealed to the box's TPM, relative to the root.
const credentialFile = "etc/credstore.encrypted/concierge-secrets-master"

// Create writes a backup of the box under root to out: it refuses while the box is busy, stops the
// services (a stopped service's files are consistent), writes the archive, then starts the core
// again or, with stop, leaves them all stopped and records the backup's ID in Marker. The header
// returned is the archive's but for its Paths, which Write fills in.
func Create(sys System, root string, out io.Writer, stop bool) (Header, error) {
	if err := idle(sys, root); err != nil {
		return Header{}, err
	}
	version, revision, err := build(root)
	if err != nil {
		return Header{}, err
	}
	box, err := os.Hostname()
	if err != nil {
		return Header{}, err
	}
	id, err := newID()
	if err != nil {
		return Header{}, err
	}
	recovery, err := exists(filepath.Join(root, RecoveryFile))
	if err != nil {
		return Header{}, err
	}
	h := Header{
		Format: 1, ID: id, Version: version, Revision: revision, Box: box,
		TakenAt: time.Now().UTC(), Recovery: recovery,
	}
	if err := sys.Stop(Units); err != nil {
		return Header{}, err
	}
	if err := Write(out, h, root); err != nil {
		// A failed backup leaves the box running, whatever was asked.
		return Header{}, errors.Join(fmt.Errorf("writing the backup: %w", err), sys.Start(CoreUnits))
	}
	if !stop {
		if err := sys.Start(CoreUnits); err != nil {
			return Header{}, fmt.Errorf("the backup is written, but starting the services failed: %w", err)
		}
		return h, nil
	}
	if err := writeMarker(root, h.ID); err != nil {
		return Header{}, fmt.Errorf("the backup is written and the services stopped, but recording the stop failed (they start again at a reboot): %w", err)
	}
	return h, nil
}

// Resume starts the services a backup with stop left stopped, and forgets which backup that was:
// the marker goes first, or the units' condition on it keeps them from starting.
func Resume(sys System, root string) error {
	if err := removeMarker(root); err != nil {
		return err
	}
	return sys.Start(CoreUnits)
}

// RecoveryCode makes a new recovery code and locks a copy of the TPM-sealed master key with it at
// RecoveryFile, replacing the copy (and so the code) of any earlier run. The code is returned once,
// for the owner's terminal alone.
func RecoveryCode(sys System, root string) (code string, replaced bool, err error) {
	key, err := sys.Decrypt(filepath.Join(root, credentialFile))
	if err != nil {
		return "", false, err
	}
	defer clear(key)
	if code, err = NewCode(); err != nil {
		return "", false, err
	}
	locked, err := Lock(key, code)
	if err != nil {
		return "", false, err
	}
	file := filepath.Join(root, RecoveryFile)
	if replaced, err = exists(file); err != nil {
		return "", false, err
	}
	if err := writeAtomic(file, locked); err != nil {
		return "", false, err
	}
	return code, replaced, nil
}

// idle refuses while a run or a call is in progress.
func idle(sys System, root string) error {
	reason, err := Busy(sys, root)
	if err != nil {
		return err
	}
	if reason != "" {
		return fmt.Errorf("the box is busy: %s", reason)
	}
	return nil
}

// build is the version and revision deployed under root.
func build(root string) (version, revision string, err error) {
	if version, err = readTrimmed(filepath.Join(root, "opt/concierge/VERSION")); err != nil {
		return "", "", err
	}
	if revision, err = readTrimmed(filepath.Join(root, "opt/concierge/REVISION")); err != nil {
		return "", "", err
	}
	return version, revision, nil
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// newID is 16 random bytes in hex.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// stateDir makes root's backupDir, 0700, when it is missing.
func stateDir(root string) (string, error) {
	dir := filepath.Join(root, backupDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, os.Chmod(dir, 0o700)
}

func writeMarker(root, id string) error {
	if _, err := stateDir(root); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(root, Marker), []byte(id))
}

func readMarker(root string) (string, error) {
	id, err := readTrimmed(filepath.Join(root, Marker))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return id, err
}

func removeMarker(root string) error {
	if err := os.Remove(filepath.Join(root, Marker)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// writeAtomic writes body to path, 0600: a temp file in path's directory, renamed over it.
func writeAtomic(path string, body []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op once renamed
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
