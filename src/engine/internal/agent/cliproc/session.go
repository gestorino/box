package cliproc

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

// settle is how long a terminal's output must pause before its screen is judged: a frame drawn
// in several reads is judged whole.
var settle = 150 * time.Millisecond

// Session is one running login command, started by the caller as a process group leader, and the
// output it has printed so far.
type Session struct {
	cmd  *exec.Cmd
	src  io.Closer // the output's source, closed by Kill
	once sync.Once

	mu   sync.Mutex
	buf  bytes.Buffer
	term *vt.Emulator // the screen, for a command on a terminal (NewTermSession)
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
	return newSession(cmd, r, nil)
}

// NewTermSession collects what cmd (already started on the terminal ptmx of cols×rows, a group
// leader) shows on that terminal: a full-screen CLI draws its frames with cursor moves, not lines,
// so its text is the screen's rows. A Finish types on ptmx.
func NewTermSession(cmd *exec.Cmd, ptmx *os.File, cols, rows int) *Session {
	term := vt.NewEmulator(cols, rows)
	// The terminal's answers to the CLI's queries are dropped: it is a terminal that answers none.
	go io.Copy(io.Discard, term)
	s := newSession(cmd, ptmx, term)
	s.Input = ptmx
	return s
}

func newSession(cmd *exec.Cmd, r io.ReadCloser, term *vt.Emulator) *Session {
	s := &Session{cmd: cmd, src: r, term: term, ping: make(chan struct{}, 1), eof: make(chan struct{}), exit: make(chan struct{})}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := r.Read(b)
			s.mu.Lock()
			if term != nil {
				term.Write(b[:n])
			} else {
				s.buf.Write(b[:n])
			}
			s.mu.Unlock()
			select {
			case s.ping <- struct{}{}:
			default:
			}
			if err != nil {
				if term != nil {
					// Ends the answers' drain; the screen stays readable. Not term.Close: it flags
					// the emulator closed unguarded while the drain reads it.
					term.InputPipe().(io.Closer).Close()
				}
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

// Text is the output so far, without terminal control sequences or carriage returns; on a
// terminal, the rows scrolled off and the screen's, one line each.
func (s *Session) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return ansi.ReplaceAllString(strings.ReplaceAll(s.buf.String(), "\r", ""), "")
	}
	var b strings.Builder
	for _, l := range s.term.Scrollback().Lines() {
		b.WriteString(l.String())
		b.WriteByte('\n')
	}
	b.WriteString(s.term.String())
	return b.String()
}

// Await returns the output once ok accepts it, or false when the output ends without it, the
// timeout passes or ctx ends. ok judges whole text: from a pipe, a line counts only when it is
// terminated; on a terminal, the screen is judged once its output pauses, or as it was left.
func (s *Session) Await(ctx context.Context, timeout time.Duration, ok func(text string) bool) (string, bool) {
	deadline := time.After(timeout)
	var quiet <-chan time.Time
	if s.term != nil {
		quiet = time.After(settle)
	}
	for {
		if s.term == nil {
			if t := s.Text(); ok(t) {
				return t, true
			}
		}
		select {
		case <-s.ping:
			if s.term != nil {
				quiet = time.After(settle)
			}
		case <-quiet:
			if t := s.Text(); ok(t) {
				return t, true
			}
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
