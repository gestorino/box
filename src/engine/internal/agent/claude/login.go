package claude

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/creack/pty"

	agentv1 "concierge/engine/internal/gen/agent/v1"
)

var loginWait = 60 * time.Second

var (
	ansi  = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)
	urlRe = regexp.MustCompile(`https://\S+\s`)
	// The token counts only once its line is complete: a read may split it.
	tokenRe  = regexp.MustCompile(`(sk-ant-\S+)\n`)
	errNoJob = connect.NewError(connect.CodeFailedPrecondition, errors.New("no sign-in in progress"))
)

// Login signs the CLI in: `claude setup-token` under a terminal, its code typed by the owner.
type Login struct {
	bin, home string

	mu  sync.Mutex
	cur *session
}

// NewLogin returns a Login of the claude binary bin, keeping the token under home.
func NewLogin(bin, home string) *Login { return &Login{bin: bin, home: home} }

// session is one running setup-token: its terminal's output so far.
type session struct {
	cmd  *exec.Cmd
	pty  *os.File
	mu   sync.Mutex
	buf  bytes.Buffer
	ping chan struct{}
	done chan struct{}
}

func (s *session) pump() {
	b := make([]byte, 4096)
	for {
		n, err := s.pty.Read(b)
		s.mu.Lock()
		s.buf.Write(b[:n])
		s.mu.Unlock()
		select {
		case s.ping <- struct{}{}:
		default:
		}
		if err != nil {
			close(s.done)
			return
		}
	}
}

// wait returns what find extracts from the output so far, as soon as it extracts something.
func (s *session) wait(ctx context.Context, find func(string) string) (string, bool) {
	timeout := time.After(loginWait)
	for {
		s.mu.Lock()
		got := find(ansi.ReplaceAllString(strings.ReplaceAll(s.buf.String(), "\r", ""), ""))
		s.mu.Unlock()
		if got != "" {
			return got, true
		}
		select {
		case <-s.ping:
		case <-s.done:
			s.mu.Lock()
			got = find(ansi.ReplaceAllString(strings.ReplaceAll(s.buf.String(), "\r", ""), ""))
			s.mu.Unlock()
			return got, got != ""
		case <-timeout:
			return "", false
		case <-ctx.Done():
			return "", false
		}
	}
}

func (s *session) kill() {
	syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL) // the session leader's whole group
	s.pty.Close()
	s.cmd.Wait()
}

// Start begins a sign-in and returns the link the owner opens. A start while one is pending ends the old one.
func (l *Login) Start(ctx context.Context) (*agentv1.LoginStartResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.drop()
	cmd := exec.Command(l.bin, "setup-token")
	cmd.Env = append(cliEnv(), "DISABLE_AUTOUPDATER=1", "HOME="+l.home)
	if err := os.MkdirAll(l.home, 0o700); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the agent's home is not writable"))
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 1000})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("starting the sign-in failed"))
	}
	s := &session{cmd: cmd, pty: ptmx, ping: make(chan struct{}, 1), done: make(chan struct{})}
	go s.pump()
	url, ok := s.wait(ctx, func(out string) string {
		return strings.TrimSpace(urlRe.FindString(out))
	})
	if !ok {
		s.kill()
		return nil, connect.NewError(connect.CodeInternal, errors.New("the sign-in gave no link"))
	}
	l.cur = s
	return &agentv1.LoginStartResponse{Url: url, NeedsCode: true}, nil
}

// Finish types the owner's code to the pending sign-in and keeps the token it prints.
func (l *Login) Finish(ctx context.Context, code string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.cur
	if s == nil {
		return errNoJob
	}
	l.cur = nil
	defer s.kill()
	if _, err := s.pty.WriteString(strings.TrimSpace(code) + "\r"); err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("the sign-in ended before the code"))
	}
	token, ok := s.wait(ctx, func(out string) string {
		if m := tokenRe.FindStringSubmatch(out); m != nil {
			return m[1]
		}
		return ""
	})
	if !ok {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the code was not accepted, start again"))
	}
	if err := writeToken(l.home, token); err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("keeping the token failed"))
	}
	return nil
}

// Logout forgets the token and ends a pending sign-in.
func (l *Login) Logout(context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.drop()
	if err := os.Remove(filepath.Join(l.home, "token")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return connect.NewError(connect.CodeInternal, errors.New("removing the token failed"))
	}
	return nil
}

// Status says whether a token is kept, and the CLI's version.
func (l *Login) Status(ctx context.Context) (*agentv1.StatusResponse, error) {
	_, err := os.Stat(filepath.Join(l.home, "token"))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, l.bin, "--version")
	cmd.Env = append(cliEnv(), "DISABLE_AUTOUPDATER=1", "HOME="+l.home)
	out, verr := cmd.Output()
	if verr != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("claude --version failed"))
	}
	return &agentv1.StatusResponse{SignedIn: err == nil, CliVersion: strings.TrimSpace(string(out))}, nil
}

func (l *Login) drop() {
	if l.cur != nil {
		l.cur.kill()
		l.cur = nil
	}
}

// writeToken keeps the token in home/token, 0600, atomically.
func writeToken(home, token string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, "token-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(token); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(home, "token"))
}
