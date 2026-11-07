package codex

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"

	agentv1 "concierge/engine/internal/gen/agent/v1"
)

// Variables so tests can shorten them.
var (
	loginWait  = 60 * time.Second
	finishWait = 15 * time.Minute
)

var (
	ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)
	// A line counts only once it is complete: a read may split it.
	urlRe    = regexp.MustCompile(`(https://\S+)\n`)
	codeRe   = regexp.MustCompile(`(?m)^[^\n]*?\b([A-Z0-9]{4,}-[A-Z0-9]{4,})\s*\n`)
	errNoJob = connect.NewError(connect.CodeFailedPrecondition, errors.New("no sign-in in progress"))
)

// Login signs the CLI in with the device flow: `codex login --device-auth` prints a link and a
// code, the owner enters the code there, and the command ends once the CLI holds the login.
type Login struct {
	bin, home string

	mu  sync.Mutex
	cur *session
}

// NewLogin returns a Login of the codex binary bin, keeping the login under home.
func NewLogin(bin, home string) *Login { return &Login{bin: bin, home: home} }

// session is one running device login: its output so far.
type session struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	buf  bytes.Buffer
	ping chan struct{}
	done chan struct{} // closed when the command has ended
	err  error         // its result, valid after done
}

// Write collects the command's output (stdout and stderr both).
func (s *session) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.buf.Write(p)
	s.mu.Unlock()
	select {
	case s.ping <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (s *session) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ansi.ReplaceAllString(strings.ReplaceAll(s.buf.String(), "\r", ""), "")
}

// waitLines returns the link and the code once both are in complete lines.
func (s *session) waitLines(ctx context.Context) (url, code string, ok bool) {
	find := func() (string, string, bool) {
		out := s.text()
		u, c := urlRe.FindStringSubmatch(out), codeRe.FindStringSubmatch(out)
		if u == nil || c == nil {
			return "", "", false
		}
		return u[1], c[1], true
	}
	timeout := time.After(loginWait)
	for {
		if u, c, ok := find(); ok {
			return u, c, true
		}
		select {
		case <-s.ping:
		case <-s.done:
			return find()
		case <-timeout:
			return "", "", false
		case <-ctx.Done():
			return "", "", false
		}
	}
}

func (s *session) kill() {
	select {
	case <-s.done:
		return
	default:
	}
	syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	<-s.done
}

// Start begins a sign-in and returns the link and the code the owner enters there. A start while
// one is pending ends the old one.
func (l *Login) Start(ctx context.Context) (*agentv1.LoginStartResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.drop()
	if err := os.MkdirAll(codexHome(l.home), 0o700); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the agent's home is not writable"))
	}
	cmd := exec.Command(l.bin, "login", "--device-auth")
	cmd.Env = cliEnv(l.home)
	s := &session{cmd: cmd, ping: make(chan struct{}, 1), done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = s, s
	ownGroup(cmd, waitDelay)
	if err := cmd.Start(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("starting the sign-in failed"))
	}
	go func() {
		s.err = cmd.Wait()
		close(s.done)
	}()
	url, code, ok := s.waitLines(ctx)
	if !ok {
		s.kill()
		return nil, connect.NewError(connect.CodeInternal, errors.New("the sign-in gave no link"))
	}
	l.cur = s
	return &agentv1.LoginStartResponse{Url: url, Code: code}, nil
}

// Finish waits for the pending sign-in to end: the owner has entered the code. The code argument
// is unused (the device flow needs none typed here).
func (l *Login) Finish(ctx context.Context, _ string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.cur
	if s == nil {
		return errNoJob
	}
	l.cur = nil
	defer s.kill()
	select {
	case <-s.done:
	case <-time.After(finishWait):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the code was not entered in time, start again"))
	case <-ctx.Done():
		return connect.NewError(connect.CodeCanceled, errors.New("the sign-in was cancelled"))
	}
	if s.err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the sign-in was not accepted, start again"))
	}
	return nil
}

// Logout ends a pending sign-in, runs `codex logout` and removes the daemon's CODEX_HOME.
func (l *Login) Logout(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.drop()
	if _, err := os.Stat(codexHome(l.home)); err == nil {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, l.bin, "logout")
		cmd.Env = cliEnv(l.home)
		killGroup(cmd, waitDelay)
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
	killGroup(st, waitDelay)
	signedIn := st.Run() == nil
	ver := exec.CommandContext(ctx, l.bin, "--version")
	ver.Env = cliEnv(l.home)
	killGroup(ver, waitDelay)
	out, err := ver.Output()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("codex --version failed"))
	}
	return &agentv1.StatusResponse{SignedIn: signedIn, CliVersion: strings.TrimSpace(string(out))}, nil
}

func (l *Login) drop() {
	if l.cur != nil {
		l.cur.kill()
		l.cur = nil
	}
}
