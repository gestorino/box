package backup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// System is every command the backup runs on the box. Create and Restore
// reach the box only through it, so their tests run on a fake.
type System interface {
	Stop(units []string) error
	Start(units []string) error
	Active(unit string) (bool, error)
	// Decrypt returns the TPM-sealed credential at credPath.
	Decrypt(credPath string) ([]byte, error)
	// Encrypt seals key to the TPM into credPath.
	Encrypt(key []byte, credPath string) error
	// Calls is mmcli's raw list of voice calls ("" when the box has no modem).
	Calls() (string, error)
	// SyncVenv rebuilds the worker's virtualenv from browserDir's lock file.
	SyncVenv(browserDir string) error
}

// Units are the box's services and sockets in stop order: the engine first and
// secretd (its service, then its socket) last, so nothing that reads secrets
// outlives it. A backup or restore stops them all; it starts only CoreUnits.
// sensord is among them: it reads the state directories (CAP_DAC_READ_SEARCH)
// through mounts made on the ones it started with, so it must not outlive a swap.
var Units = []string{
	"concierge-engine.service",
	"concierge-worker.service",
	"concierge-hostd.service",
	"concierge-hostd.socket",
	"concierge-hostd-settings.socket",
	"concierge-sms.service",
	"concierge-sms.socket",
	"concierge-calls.service",
	"concierge-calls.socket",
	"concierge-whatsapp.service",
	"concierge-whatsapp.socket",
	"concierge-telegram.service",
	"concierge-telegram.socket",
	"concierge-email.service",
	"concierge-email.socket",
	"concierge-sensors.service",
	"concierge-secrets.service",
	"concierge-secrets.socket",
}

// CoreUnits are what a backup or restore starts again, in start order: secretd first, the engine
// last. hostd then starts the connectors the owner added, and keeps the rest stopped, as at boot.
// While Marker exists none of the units starts (each has ConditionPathExists=! on it), reboot
// included, so it is removed before they are started.
var CoreUnits = []string{
	"concierge-secrets.socket",
	"concierge-secrets.service",
	"concierge-hostd.socket",
	"concierge-hostd-settings.socket",
	"concierge-hostd.service",
	"concierge-sensors.service",
	"concierge-worker.service",
	"concierge-engine.service",
}

const credName = "--name=concierge-secrets-master"

// Exec is the real System: it runs the box's commands. It is exercised only
// on the box. The master key passes through pipes alone, never an argument,
// the environment or a file of ours, and is never logged.
type Exec struct{}

var _ System = Exec{}

func (Exec) Stop(units []string) error  { return systemctl("stop", units) }
func (Exec) Start(units []string) error { return systemctl("start", units) }

func systemctl(verb string, units []string) error {
	return run(exec.Command("systemctl", append([]string{verb}, units...)...), verb)
}

// Active is systemctl is-active: it exits non-zero for anything not active.
func (Exec) Active(unit string) (bool, error) {
	out, err := exec.Command("systemctl", "is-active", unit).Output()
	if strings.TrimSpace(string(out)) == "active" {
		return true, nil
	}
	var exit *exec.ExitError
	if err == nil || errors.As(err, &exit) {
		return false, nil
	}
	return false, fmt.Errorf("systemctl is-active %s: %w", unit, err)
}

func (Exec) Decrypt(credPath string) ([]byte, error) {
	cmd := exec.Command("systemd-creds", "decrypt", credName, credPath, "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	key, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("systemd-creds decrypt: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return key, nil
}

// Encrypt seals key to the TPM with no PCRs, the key on stdin.
func (Exec) Encrypt(key []byte, credPath string) error {
	cmd := exec.Command("systemd-creds", "encrypt", "--with-key=tpm2", "--tpm2-pcrs=", credName, "-", credPath)
	cmd.Stdin = bytes.NewReader(key)
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	return run(cmd, "systemd-creds encrypt")
}

func (Exec) Calls() (string, error) {
	out, err := exec.Command("mmcli", "-m", "any", "--voice-list-calls").CombinedOutput()
	if err != nil {
		if isNoModem(string(out)) {
			return "", nil
		}
		return "", fmt.Errorf("mmcli: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// SyncVenv rebuilds the venv the way the deploy builds it.
func (Exec) SyncVenv(browserDir string) error {
	cmd := exec.Command("/usr/local/bin/uv", "sync", "--frozen", "--no-dev", "--directory", browserDir)
	cmd.Env = append(os.Environ(),
		"UV_PROJECT_ENVIRONMENT=/opt/concierge/venv",
		"UV_PYTHON_INSTALL_DIR=/opt/concierge/python",
		"UV_PYTHON=3.12",
		"UV_COMPILE_BYTECODE=1",
	)
	return run(cmd, "uv sync")
}

// run runs cmd and names its output in the error. Never used with a command
// that prints a secret.
func run(cmd *exec.Cmd, what string) error {
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", what, err, strings.TrimSpace(string(out)))
	}
	return nil
}
