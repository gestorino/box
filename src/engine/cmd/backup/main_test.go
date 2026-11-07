package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"concierge/engine/internal/backup"
)

func runCmd(args []string, uid int) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, uid, nil, &out, &errb, true)
	return code, out.String(), errb.String()
}

// refusingSystem is a System whose every command fails, and that records which were asked.
type refusingSystem struct{ calls []string }

func (s *refusingSystem) refuse(c string) error {
	s.calls = append(s.calls, c)
	return errors.New(c + " refused")
}

func (s *refusingSystem) Hold() func()                   { s.calls = append(s.calls, "Hold"); return func() {} }
func (s *refusingSystem) Stop([]string) error            { return s.refuse("Stop") }
func (s *refusingSystem) Start([]string) error           { return s.refuse("Start") }
func (s *refusingSystem) Active(string) (bool, error)    { return false, s.refuse("Active") }
func (s *refusingSystem) Decrypt(string) ([]byte, error) { return nil, s.refuse("Decrypt") }
func (s *refusingSystem) Encrypt([]byte, string) error   { return s.refuse("Encrypt") }
func (s *refusingSystem) Calls() (string, error)         { return "", s.refuse("Calls") }
func (s *refusingSystem) SyncVenv(string) error          { return s.refuse("SyncVenv") }

var _ backup.System = (*refusingSystem)(nil)

// The code is shown once, on the owner's terminal: never into a file or a pipe.
func TestRecoveryCodeNeedsATerminal(t *testing.T) {
	sys := &refusingSystem{}
	var out, errb bytes.Buffer
	code := run([]string{"recovery-code"}, 0, sys, &out, &errb, false)
	if code != 1 || out.Len() != 0 || !strings.Contains(errb.String(), "terminal") || !strings.Contains(errb.String(), "make recovery-code") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
	if sys.calls != nil {
		t.Fatalf("asked the box %q before refusing", sys.calls)
	}
	// On a terminal it goes ahead (to the TPM, which refuses here).
	if code := run([]string{"recovery-code"}, 0, sys, &out, &errb, true); code != 1 || len(sys.calls) != 1 || sys.calls[0] != "Decrypt" {
		t.Fatalf("on a terminal: code %d, calls %q", code, sys.calls)
	}
}

// mainChild makes TestBrokenPipeIsAnError's process the child: it runs main with the given args.
const mainChild = "BACKUP_TEST_MAIN_CHILD"

// A reader that goes away (the Mac's ssh) makes a write fail instead of killing the command: a
// create that dies of SIGPIPE leaves every service stopped. -version writes to a pipe whose reader
// is gone, and the command must exit as it chooses, not of the signal.
func TestBrokenPipeIsAnError(t *testing.T) {
	if os.Getenv(mainChild) != "" {
		os.Args = []string{"backup", "-version"}
		main()
		return
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestBrokenPipeIsAnError$")
	cmd.Env = append(os.Environ(), mainChild+"=1")
	cmd.Stdout = w
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err = cmd.Run()
	w.Close()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if ws := exit.Sys().(syscall.WaitStatus); ws.Signaled() {
			t.Fatalf("killed by %v writing to a closed pipe\n%s", ws.Signal(), errb.String())
		}
	}
}

func TestRefusesWithoutRoot(t *testing.T) {
	code, out, errs := runCmd([]string{"create"}, 1000)
	if code != 1 || out != "" || !strings.Contains(errs, "root") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
	}
}

func TestUnknownSubcommand(t *testing.T) {
	code, _, errs := runCmd([]string{"frobnicate"}, 0)
	if code != 1 || !strings.Contains(errs, `unknown command "frobnicate"`) {
		t.Fatalf("code %d, stderr %q", code, errs)
	}
}

func TestUsageNamesEverySubcommand(t *testing.T) {
	code, _, errs := runCmd(nil, 0)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	for _, s := range []string{"create", "resume", "recovery-code", "restore", "install-build"} {
		if !strings.Contains(errs, s) {
			t.Errorf("usage does not name %q: %s", s, errs)
		}
	}
}

func TestRestoreNeedsFrom(t *testing.T) {
	for _, c := range []string{"restore", "install-build"} {
		code, _, errs := runCmd([]string{c}, 0)
		if code != 1 || !strings.Contains(errs, "-from") {
			t.Errorf("%s: code %d, stderr %q", c, code, errs)
		}
	}
}

func TestInstallBuildNeedsFor(t *testing.T) {
	code, _, errs := runCmd([]string{"install-build", "-from", "/x.tar.gz"}, 0)
	if code != 1 || !strings.Contains(errs, "-for ID") {
		t.Fatalf("code %d, stderr %q", code, errs)
	}
}

func TestVersionNeedsNoRoot(t *testing.T) {
	code, out, _ := runCmd([]string{"-version"}, 1000)
	if code != 0 || !strings.Contains(out, "dev") {
		t.Fatalf("code %d, stdout %q", code, out)
	}
}
