// Package cliproc is what the agent daemons share in running a vendor's CLI: a clean environment,
// a process group that dies whole, an error that carries stderr's first line only, and a login
// session that collects the CLI's output until the owner's link, code or token shows in it.
package cliproc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
)

// Env is this process's environment without any variable that starts with a prefix in drop, and
// without the variables set names, followed by set (each "KEY=value").
func Env(drop []string, set ...string) []string {
	var env []string
next:
	for _, kv := range os.Environ() {
		for _, p := range drop {
			if strings.HasPrefix(kv, p) {
				continue next
			}
		}
		for _, s := range set {
			if k, _, _ := strings.Cut(s, "="); strings.HasPrefix(kv, k+"=") {
				continue next
			}
		}
		env = append(env, kv)
	}
	return append(env, set...)
}

// OwnGroup puts cmd in a process group of its own, so a group kill reaches its children too, and
// bounds how long Wait waits on pipes a grandchild still holds.
func OwnGroup(cmd *exec.Cmd, waitDelay time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
}

// KillOnCancel is OwnGroup for a command made with a context: the context's end kills the group.
func KillOnCancel(cmd *exec.Cmd, waitDelay time.Duration) {
	OwnGroup(cmd, waitDelay)
	cmd.Cancel = func() error { KillGroup(cmd.Process.Pid); return nil }
}

// KillGroup kills every process of the group led by pid, until none is left: a child the leader
// forked as the first signal arrived joins the group after it and is caught by the next.
func KillGroup(pid int) {
	for i := 0; i < 100; i++ {
		if syscall.Kill(-pid, syscall.SIGKILL) != nil { // ESRCH: the group is empty
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Fail is an Internal error naming what went wrong and stderr's first line, never the input.
// Each secret is replaced in that line.
func Fail(what, stderr string, err error, secrets ...string) error {
	line, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n")
	line = strings.TrimSpace(line)
	for _, s := range secrets {
		if s != "" {
			line = strings.ReplaceAll(line, s, "[secret]")
		}
	}
	msg := what
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg += fmt.Sprintf(" (%s)", ee.ProcessState)
	} else if err != nil {
		msg += " (" + err.Error() + ")"
	}
	if line != "" {
		msg += ": " + line
	}
	return connect.NewError(connect.CodeInternal, errors.New(msg))
}
