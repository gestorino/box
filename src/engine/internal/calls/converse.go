package calls

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/realtime"
)

// Talker is a voice model's session on the line (*realtime.Session): it takes the other party's audio
// (μ-law), speaks when asked, and reports what it heard, what it said and the tokens it used.
type Talker interface {
	Append(ulaw []byte) error
	Respond(ctx context.Context, instructions string) error
	Audio() <-chan []byte
	Heard() <-chan string
	Said() <-chan string
	Spoken() <-chan struct{}
	Done() <-chan struct{}
	Usage() realtime.Usage
	ToolCalls() <-chan realtime.ToolCall
	Close() error
}

// Script runs the conversation once the call is answered: outcome is passed, wrong or failed;
// summary is the call in a line or three, for the log. It must return when ctx ends.
type Script func(ctx context.Context, t Talker) (outcome, summary string)

// Held is what the owner asks of one held call: how long it may last from its answer, and the
// prompt it was placed with (kept on the call, "" for the check).
type Held struct {
	Limit  time.Duration
	Prompt string
}

// Kept is what a held call leaves behind: always its summary and the tokens its session used; its
// turns and its recording (a file in the Caller's tmp, "" if none) only when the owner's settings
// said so as the call started.
type Kept struct {
	Summary string
	Result  string // the script's outcome
	Prompt  string // what the owner asked of the call, if they did
	Usage   *CallUsage
	Turns   []Turn
	Audio   string
}

// chunk is 20 ms of the call's audio as the sound card gives it (S16_LE, 8 kHz).
const chunk = 320

// Converse places a call held by a voice model. The session opens before the dial, so a model that
// can't be reached never rings anyone. Answered, the call's audio flows both ways between the sound
// card and the session while script runs; it ends when the script returns, the other party hangs up,
// the session drops, or h.Limit passes — and then the box waits for its last words to play, keeps
// what the call left behind, unroutes and hangs up. Unanswered, it returns busy, no-answer or failed,
// and keeps nothing. An error means no call was placed.
//
// What the owner lets a call keep is read once, before the dial, so a switch flipped mid-call never
// leaves it half-kept; until hostd first answers, nothing beyond the summary is kept.
func (c *Caller) Converse(ctx context.Context, number string, open func(context.Context) (Talker, error), script Script, h Held) (string, error) {
	if !c.busy.TryLock() {
		return "", ErrBusy
	}
	defer c.busy.Unlock()
	keep, known := c.settings()
	if !known {
		keep = hostsettings.CallsKeep{} // never kept on a guess
	}
	t, err := open(ctx)
	if err != nil {
		return "", err
	}
	defer t.Close()
	path, out, finish, err := c.connect(ctx, number)
	if err != nil {
		return "", err
	}
	defer finish()
	if out != "" {
		return out, nil
	}

	cctx, cancel := context.WithTimeout(ctx, h.Limit)
	defer cancel()
	go c.watch(cctx, cancel, path, t)
	in, speaker, err := c.d.Open(cctx)
	if err != nil {
		slog.Error("calls: opening the sound card both ways", "err", err)
		return "failed", nil
	}
	var f *os.File
	var rec *Recorder
	if keep.Audio {
		f, rec = c.record()
	}
	talker := t
	var tr *transcript
	if keep.Transcript {
		tr = transcribe(cctx, t, c.now)
		talker = tr
	}
	p := &player{w: speaker, rec: rec}
	finished, played := make(chan struct{}), make(chan struct{})
	var listening sync.WaitGroup
	listening.Add(1)
	go func() { defer listening.Done(); listen(cctx, in, t, rec, finished) }()
	go func() { defer close(played); p.play(cctx, t.Audio(), finished) }()

	outcome, summary := script(cctx, talker)
	close(finished) // the last words reach the other party before the hangup
	select {
	case <-played:
	case <-cctx.Done():
	}
	cancel()
	t.Close() // first: a pump stuck writing to a stalled socket is freed, so the hangup never waits on it
	in.Close()
	speaker.Close()
	listening.Wait()
	<-played
	k := Kept{Summary: summary, Result: outcome, Prompt: h.Prompt, Usage: costed(t.Usage())}
	if tr != nil {
		k.Turns = tr.turns()
	}
	if rec != nil { // both sides have stopped writing to it
		k.Audio = finishRecording(f, rec)
	}
	if k.Summary != "" || k.Result != "" || k.Usage != nil || k.Turns != nil || k.Audio != "" {
		c.keep(path, k) // before the deferred release, so the archiver still holds the call
	}
	return outcome, nil
}

// record starts the call's recording in tmp, owner-only (CreateTemp makes it 0600). Nil when it could
// not: the call goes on unrecorded rather than not at all.
func (c *Caller) record() (*os.File, *Recorder) {
	f, err := os.CreateTemp(c.tmp, "call-*.wav")
	if err != nil {
		slog.Error("calls: starting the call's recording", "err", err)
		return nil, nil
	}
	rec, err := NewRecorder(f)
	if err != nil {
		slog.Error("calls: starting the call's recording", "err", err)
		f.Close()
		os.Remove(f.Name())
		return nil, nil
	}
	return f, rec
}

// finishRecording fixes the recording's header and closes it: its path, or "" (and no file) when
// that failed.
func finishRecording(f *os.File, rec *Recorder) string {
	err := rec.Close()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		slog.Error("calls: finishing the call's recording", "err", err)
		os.Remove(f.Name())
		return ""
	}
	return f.Name()
}

// transcript is a Talker that keeps the conversation turn by turn, in the order it happens, without
// the script changing shape for it: the other party's turns as Heard gives them (each still reaching
// the script), the box's as the session says it spoke them — not the instructions it was given, so a
// goodbye spoken in the wrong language shows as spoken.
type transcript struct {
	Talker
	now   func() time.Time
	heard chan string
	said  chan string

	following sync.WaitGroup
	mu        sync.Mutex
	kept      []Turn
}

// transcribe follows t's Heard and Said until ctx ends.
func transcribe(ctx context.Context, t Talker, now func() time.Time) *transcript {
	tr := &transcript{Talker: t, now: now, heard: make(chan string, 8), said: make(chan string, 16)}
	tr.following.Add(2)
	go tr.follow(ctx, t.Heard(), "them", tr.heard, true)
	go tr.follow(ctx, t.Said(), "box", tr.said, false)
	return tr
}

func (tr *transcript) Heard() <-chan string { return tr.heard }
func (tr *transcript) Said() <-chan string  { return tr.said }

// follow records each text from in as speaker's turn and passes it on: an answer waits for the
// script to take it, as the session's own Heard does; what the box said is dropped when nobody reads
// it, as the session's own Said does. Once ctx ends, what is already queued is still recorded.
func (tr *transcript) follow(ctx context.Context, in <-chan string, speaker string, out chan<- string, wait bool) {
	defer tr.following.Done()
	for {
		select {
		case text, ok := <-in:
			if !ok {
				return
			}
			tr.add(speaker, text)
			if !wait {
				select {
				case out <- text:
				default:
				}
				continue
			}
			select {
			case out <- text:
			case <-ctx.Done():
				tr.drain(in, speaker)
				return
			}
		case <-ctx.Done():
			tr.drain(in, speaker)
			return
		}
	}
}

func (tr *transcript) drain(in <-chan string, speaker string) {
	for {
		select {
		case text, ok := <-in:
			if !ok {
				return
			}
			tr.add(speaker, text)
		default:
			return
		}
	}
}

func (tr *transcript) add(speaker, text string) {
	tr.mu.Lock()
	tr.kept = append(tr.kept, Turn{Speaker: speaker, Text: text, At: ts(tr.now())})
	tr.mu.Unlock()
}

// turns waits for both sides to stop following (the conversation's ctx has ended) and returns them.
func (tr *transcript) turns() []Turn {
	tr.following.Wait()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.kept
}

// watch ends the conversation when the call ends or the session drops.
func (c *Caller) watch(ctx context.Context, cancel context.CancelFunc, path string, t Talker) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.Done():
			cancel()
			return
		case <-time.After(c.poll):
		}
		if state, _, ok := c.state(ctx, path); ok && (state == "terminated" || state == "gone") {
			cancel()
			return
		}
	}
}

// listen sends the other party's voice to the session as it comes, up to 20 ms at a time; a half
// sample left over waits for the next read. rec, if set, records the same samples as read, before
// they are converted for the session. Once finished closes the outcome is decided: the voice is still
// read (and recorded) but no longer sent, so a model that leads starts no reply nobody supervises.
func listen(ctx context.Context, in io.Reader, t Talker, rec *Recorder, finished <-chan struct{}) {
	buf := make([]byte, chunk)
	carry := 0
	for ctx.Err() == nil {
		n, err := in.Read(buf[carry:])
		total := carry + n
		whole := total &^ 1
		if whole > 0 {
			if rec != nil {
				if err := rec.Them(buf[:whole]); err != nil {
					slog.Error("calls: recording the call", "err", err)
					rec = nil // the call goes on unrecorded
				}
			}
			select {
			case <-finished:
			default:
				if err := t.Append(toULaw(buf[:whole])); err != nil {
					return
				}
			}
		}
		carry = copy(buf, buf[whole:total])
		if err != nil {
			return
		}
	}
}

// player writes the model's voice to the sound card and knows when the last of it will have played:
// the card plays 8000 samples a second, however fast they were written. rec, if set, records exactly
// what reached the card.
type player struct {
	w     io.Writer
	rec   *Recorder
	until time.Time
}

// play writes the model's voice as it comes. Once finished closes it writes what is still queued —
// every word of a response is queued before the response is reported done, though the session may
// still be handing it over — and returns when the last of it has played.
func (p *player) play(ctx context.Context, audio <-chan []byte, finished <-chan struct{}) {
	write := func(ulaw []byte) bool {
		pcm := fromULaw(ulaw)
		if _, err := p.w.Write(pcm); err != nil {
			return false
		}
		if p.rec != nil {
			p.rec.Box(pcm)
		}
		p.until = later(p.until, time.Now()).Add(time.Duration(len(ulaw)) * time.Second / 8000)
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ulaw := <-audio:
			if !write(ulaw) {
				return
			}
		case <-finished:
			for {
				select {
				case ulaw := <-audio: // first: what is queued plays whatever the clock says
					if !write(ulaw) {
						return
					}
					continue
				default:
				}
				select {
				case ulaw := <-audio: // handed over while the last words play
					if !write(ulaw) {
						return
					}
				case <-ctx.Done():
					return
				case <-time.After(time.Until(p.until)):
					return
				}
			}
		}
	}
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
