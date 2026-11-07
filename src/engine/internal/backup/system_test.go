package backup

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// holdChild is the environment variable that makes TestExecHold's process the child: it holds,
// releases when asked, sends itself the signal and exits 0 if it is still alive.
const holdChild = "BACKUP_TEST_HOLD_CHILD"

// Exec's Hold keeps the signals that would end the command off until it is released, and the
// release gives them back their default: each case runs in a child process, which the signal kills
// or does not.
func TestExecHold(t *testing.T) {
	if os.Getenv(holdChild) != "" {
		holdAndSignal()
		return
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM} {
		for _, release := range []bool{false, true} {
			t.Run(sig.String()+map[bool]string{false: " held", true: " released"}[release], func(t *testing.T) {
				t.Parallel()
				cmd := exec.Command(os.Args[0], "-test.run=^TestExecHold$")
				cmd.Env = append(os.Environ(), holdChild+"="+strconv.Itoa(int(sig))+map[bool]string{false: "", true: ",release"}[release])
				out, err := cmd.CombinedOutput()
				var exit *exec.ExitError
				killed := errors.As(err, &exit) && exit.Sys().(syscall.WaitStatus).Signaled() &&
					exit.Sys().(syscall.WaitStatus).Signal() == sig
				if release && !killed {
					t.Fatalf("released, yet %v did not kill the process: %v\n%s", sig, err, out)
				}
				if !release && err != nil {
					t.Fatalf("held, yet %v ended the process: %v\n%s", sig, err, out)
				}
			})
		}
	}
}

func holdAndSignal() {
	n, release := strings.CutSuffix(os.Getenv(holdChild), ",release")
	sig, err := strconv.Atoi(n)
	if err != nil {
		os.Exit(3)
	}
	done := Exec{}.Hold()
	if release {
		done()
	}
	syscall.Kill(os.Getpid(), syscall.Signal(sig))
	time.Sleep(300 * time.Millisecond) // a signal that kills has done so by now
	os.Exit(0)
}
