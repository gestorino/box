package claude

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/creack/pty"

	"concierge/engine/internal/agent/cliproc"
	agentv1 "concierge/engine/internal/gen/agent/v1"
)

var loginWait = 60 * time.Second

// The terminal the sign-in runs on: wide enough that the link and the token each fit on one row.
const cols, rows = 1000, 50

var (
	// Both are read off the screen's rows, judged once drawn (cliproc.Session.Await).
	urlRe    = regexp.MustCompile(`https://\S+`)
	tokenRe  = regexp.MustCompile(`sk-ant-\S+`)
	errNoJob = connect.NewError(connect.CodeFailedPrecondition, errors.New("no sign-in in progress"))
)

// Login signs the CLI in: `claude setup-token` under a terminal, its code typed by the owner.
type Login struct {
	bin, home string
	pending   cliproc.Slot
}

// NewLogin returns a Login of the claude binary bin, keeping the token under home.
func NewLogin(bin, home string) *Login { return &Login{bin: bin, home: home} }

// Start begins a sign-in and returns the link the owner opens. A start while one is pending ends the old one.
func (l *Login) Start(ctx context.Context) (*agentv1.LoginStartResponse, error) {
	l.pending.Drop()
	if err := os.MkdirAll(l.home, 0o700); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the agent's home is not writable"))
	}
	cmd := exec.Command(l.bin, "setup-token")
	cmd.Env = cliEnv("HOME=" + l.home)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols}) // its own session and group
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("starting the sign-in failed"))
	}
	s := cliproc.NewTermSession(cmd, ptmx, cols, rows)
	l.pending.Set(s)
	out, ok := s.Await(ctx, loginWait, urlRe.MatchString)
	if !ok {
		l.pending.Release(s)
		return nil, connect.NewError(connect.CodeInternal, errors.New("the sign-in gave no link"))
	}
	return &agentv1.LoginStartResponse{Url: urlRe.FindString(out), NeedsCode: true}, nil
}

// Finish types the owner's code to the pending sign-in and keeps the token it prints. A Start or
// Logout meanwhile ends the sign-in and the wait with it.
func (l *Login) Finish(ctx context.Context, code string) error {
	s := l.pending.Claim()
	if s == nil {
		slog.Warn("claudd: a sign-in's code came with none pending")
		return errNoJob
	}
	defer l.pending.Release(s)
	// The code, then Enter once it is shown: the CLI takes Enter in the same read as a long code
	// as part of a paste, and submits nothing.
	_, err := s.Input.Write([]byte(strings.TrimSpace(code)))
	if err == nil {
		s.Await(ctx, loginWait, func(string) bool { return true })
		_, err = s.Input.Write([]byte("\r"))
	}
	if err != nil {
		slog.Warn("claudd: the sign-in ended before its code", "cli", ended(s))
		return connect.NewError(connect.CodeInternal, errors.New("the sign-in ended before the code"))
	}
	begun := time.Now()
	out, ok := s.Await(ctx, loginWait, tokenRe.MatchString)
	if !ok {
		// How it ended, never what the screen says: a token or the code could be on it.
		slog.Warn("claudd: the sign-in gave no token", "cli", ended(s), "after", time.Since(begun).Round(100*time.Millisecond),
			"token_on_screen", tokenRe.MatchString(s.Text()))
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the code was not accepted, start again"))
	}
	if err := writeToken(l.home, tokenRe.FindString(out)); err != nil {
		slog.Error("claudd: keeping the token failed", "err", err)
		return connect.NewError(connect.CodeInternal, errors.New("keeping the token failed"))
	}
	slog.Info("claudd: signed in")
	return nil
}

// ended says how the sign-in's CLI stands: its exit, or still running.
func ended(s *cliproc.Session) string {
	select {
	case <-s.Exited():
		if s.Err() == nil {
			return "exit status 0"
		}
		return s.Err().Error()
	default:
		return "running"
	}
}

// Logout forgets the token and ends a pending sign-in.
func (l *Login) Logout(context.Context) error {
	l.pending.Drop()
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
	cmd.Env = cliEnv("HOME=" + l.home)
	cliproc.KillOnCancel(cmd, waitDelay)
	out, verr := cmd.Output()
	if verr != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("claude --version failed"))
	}
	return &agentv1.StatusResponse{SignedIn: err == nil, CliVersion: strings.TrimSpace(string(out))}, nil
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
