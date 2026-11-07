package codex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"

	"concierge/engine/internal/agent/cliproc"
	agentv1 "concierge/engine/internal/gen/agent/v1"
)

// Variables so tests can shorten them.
var (
	loginWait  = 60 * time.Second
	finishWait = 15 * time.Minute
)

var (
	// A line counts only once it is complete: a read may split it.
	urlRe    = regexp.MustCompile(`(https://\S+)\n`)
	codeRe   = regexp.MustCompile(`(?m)^[^\n]*?\b([A-Z0-9]{4,}-[A-Z0-9]{4,})\s*\n`)
	errNoJob = connect.NewError(connect.CodeFailedPrecondition, errors.New("no sign-in in progress"))
)

// Login signs the CLI in with the device flow: `codex login --device-auth` prints a link and a
// code, the owner enters the code there, and the command ends once the CLI holds the login.
type Login struct {
	bin, home string
	pending   cliproc.Slot
}

// NewLogin returns a Login of the codex binary bin, keeping the login under home.
func NewLogin(bin, home string) *Login { return &Login{bin: bin, home: home} }

// Start begins a sign-in and returns the link and the code the owner enters there. A start while
// one is pending ends the old one.
func (l *Login) Start(ctx context.Context) (*agentv1.LoginStartResponse, error) {
	l.pending.Drop()
	if err := os.MkdirAll(codexHome(l.home), 0o700); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the agent's home is not writable"))
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("starting the sign-in failed"))
	}
	cmd := exec.Command(l.bin, "login", "--device-auth")
	cmd.Env = cliEnv(l.home)
	cmd.Stdout, cmd.Stderr = w, w
	cliproc.OwnGroup(cmd, waitDelay)
	err = cmd.Start()
	w.Close()
	if err != nil {
		r.Close()
		return nil, connect.NewError(connect.CodeInternal, errors.New("starting the sign-in failed"))
	}
	s := cliproc.NewSession(cmd, r)
	l.pending.Set(s)
	out, ok := s.Await(ctx, loginWait, func(t string) bool { return urlRe.MatchString(t) && codeRe.MatchString(t) })
	if !ok {
		l.pending.Release(s)
		return nil, connect.NewError(connect.CodeInternal, errors.New("the sign-in gave no link"))
	}
	return &agentv1.LoginStartResponse{Url: urlRe.FindStringSubmatch(out)[1], Code: codeRe.FindStringSubmatch(out)[1]}, nil
}

// Finish waits for the pending sign-in to end: the owner has entered the code. The code argument
// is unused (the device flow needs none typed here). A Start or Logout meanwhile ends the sign-in
// and the wait with it.
func (l *Login) Finish(ctx context.Context, _ string) error {
	s := l.pending.Claim()
	if s == nil {
		return errNoJob
	}
	defer l.pending.Release(s)
	select {
	case <-s.Exited():
	case <-time.After(finishWait):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the code was not entered in time, start again"))
	case <-ctx.Done():
		return connect.NewError(connect.CodeCanceled, errors.New("the sign-in was cancelled"))
	}
	if s.Err() != nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the sign-in was not accepted, start again"))
	}
	return nil
}

// Logout ends a pending sign-in, runs `codex logout` and removes the daemon's CODEX_HOME.
func (l *Login) Logout(ctx context.Context) error {
	l.pending.Drop()
	if _, err := os.Stat(codexHome(l.home)); err == nil {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, l.bin, "logout")
		cmd.Env = cliEnv(l.home)
		cliproc.KillOnCancel(cmd, waitDelay)
		cmd.Run() // the files go either way
	}
	if err := os.RemoveAll(codexHome(l.home)); err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("removing the login failed"))
	}
	return nil
}

// Status says whether `codex login status` finds a login, and the CLI's version.
func (l *Login) Status(ctx context.Context) (*agentv1.StatusResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st := exec.CommandContext(ctx, l.bin, "login", "status")
	st.Env = cliEnv(l.home)
	cliproc.KillOnCancel(st, waitDelay)
	signedIn := st.Run() == nil
	ver := exec.CommandContext(ctx, l.bin, "--version")
	ver.Env = cliEnv(l.home)
	cliproc.KillOnCancel(ver, waitDelay)
	out, err := ver.Output()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("codex --version failed"))
	}
	return &agentv1.StatusResponse{SignedIn: signedIn, CliVersion: strings.TrimSpace(string(out))}, nil
}
