package cliproc

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

// Session is one running login command, started by the caller as a process group leader, and the
// output it has printed so far.
type Session struct {
	cmd  *exec.Cmd
	src  io.Closer // the output's source, closed by Kill
	once sync.Once

	mu   sync.Mutex
	buf  bytes.Buffer
	ping chan struct{}
	eof  chan struct{} // closed when the output has ended
	exit chan struct{} // closed when the command has ended
	err  error         // its result, valid after exit

	// Input, when set, is where a Finish types (a terminal).
	Input io.Writer

	claimed bool // Slot: a Finish is waiting on it
}

// NewSession collects what cmd (already started, a group leader) prints to r, until r ends.
func NewSession(cmd *exec.Cmd, r io.ReadCloser) *Session {
	s := &Session{cmd: cmd, src: r, ping: make(chan struct{}, 1), eof: make(chan struct{}), exit: make(chan struct{})}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := r.Read(b)
			s.mu.Lock()
			s.buf.Write(b[:n])
			s.mu.Unlock()
			select {
			case s.ping <- struct{}{}:
			default:
			}
			if err != nil {
				close(s.eof)
				return
			}
		}
	}()
	go func() {
		s.err = cmd.Wait()
		close(s.exit)
	}()
	return s
}

// Text is the output so far, without terminal control sequences or carriage returns.
func (s *Session) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ansi.ReplaceAllString(strings.ReplaceAll(s.buf.String(), "\r", ""), "")
}

// Await returns the output once ok accepts it, or false when the output ends without it, the
// timeout passes or ctx ends. ok judges whole text: a line counts only when it is terminated.
func (s *Session) Await(ctx context.Context, timeout time.Duration, ok func(text string) bool) (string, bool) {
	deadline := time.After(timeout)
	for {
		if t := s.Text(); ok(t) {
			return t, true
		}
		select {
		case <-s.ping:
		case <-s.eof:
			t := s.Text()
			return t, ok(t)
		case <-deadline:
			return "", false
		case <-ctx.Done():
			return "", false
		}
	}
}

// Exited is closed when the command has ended; Err is then its result.
func (s *Session) Exited() <-chan struct{} { return s.exit }

// Err is the command's result, valid once Exited is closed.
func (s *Session) Err() error { return s.err }

// Kill ends the command's whole group, closes its output and waits for it to be gone.
func (s *Session) Kill() {
	s.once.Do(func() {
		KillGroup(s.cmd.Process.Pid)
		s.src.Close()
	})
	<-s.exit
}

// Slot holds the one pending login of a daemon.
type Slot struct {
	mu  sync.Mutex
	cur *Session
}

// Set makes s the pending login and kills the one it replaces.
func (p *Slot) Set(s *Session) {
	p.mu.Lock()
	old := p.cur
	p.cur = s
	p.mu.Unlock()
	if old != nil {
		old.Kill()
	}
}

// Drop kills the pending login, if any, even while a Finish waits on it.
func (p *Slot) Drop() {
	p.mu.Lock()
	old := p.cur
	p.cur = nil
	p.mu.Unlock()
	if old != nil {
		old.Kill()
	}
}

// Claim returns the pending login for a Finish, or nil when none is pending or one is claimed.
// The login stays pending: a Set or Drop meanwhile kills it and so ends the Finish.
func (p *Slot) Claim() *Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil || p.cur.claimed {
		return nil
	}
	p.cur.claimed = true
	return p.cur
}

// Release ends s, taking it out of the slot if it is still the pending one.
func (p *Slot) Release(s *Session) {
	p.mu.Lock()
	if p.cur == s {
		p.cur = nil
	}
	p.mu.Unlock()
	s.Kill()
}
