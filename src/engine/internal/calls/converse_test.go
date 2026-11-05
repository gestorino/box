package calls

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/realtime"
	"concierge/engine/internal/verify"
)

// fakeTalker is a Realtime session: every Respond is recorded and answered by a response.done;
// answers are what the owner says after each Respond (by index), "" for silence.
type fakeTalker struct {
	mu       sync.Mutex
	said     []string // the instructions Respond was given
	appended []byte
	answers  map[int]string
	closed   bool
	audio    chan []byte
	heard    chan string
	spoke    chan string // what the session says it spoke (Said)
	spoken   chan struct{}
	done     chan struct{}
	dropOnce sync.Once
	usage    realtime.Usage
	tools    chan realtime.ToolCall
}

func newTalker(answers map[int]string) *fakeTalker {
	return &fakeTalker{answers: answers, audio: make(chan []byte, 16), heard: make(chan string, 16), spoke: make(chan string, 16), spoken: make(chan struct{}, 16), done: make(chan struct{}), tools: make(chan realtime.ToolCall, 16)}
}

func (f *fakeTalker) Append(b []byte) error {
	f.mu.Lock()
	f.appended = append(f.appended, b...)
	f.mu.Unlock()
	return nil
}

func (f *fakeTalker) Respond(_ context.Context, instr string) error {
	f.mu.Lock()
	n := len(f.said)
	f.said = append(f.said, instr)
	a, ok := f.answers[n]
	f.mu.Unlock()
	f.spoken <- struct{}{}
	if ok && a != "" {
		f.heard <- a
	}
	return nil
}

func (f *fakeTalker) Audio() <-chan []byte                { return f.audio }
func (f *fakeTalker) Heard() <-chan string                { return f.heard }
func (f *fakeTalker) Said() <-chan string                 { return f.spoke }
func (f *fakeTalker) Usage() realtime.Usage               { return f.usage }
func (f *fakeTalker) ToolCalls() <-chan realtime.ToolCall { return f.tools }
func (f *fakeTalker) Spoken() <-chan struct{}             { return f.spoken }
func (f *fakeTalker) Done() <-chan struct{}               { return f.done }
func (f *fakeTalker) drop()                               { f.dropOnce.Do(func() { close(f.done) }) }
func (f *fakeTalker) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeTalker) saidSoFar() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.said...)
}

// fakeDuplex is the sound card: what the test writes to caller reaches calld as the caller's
// voice; what calld plays collects in played.
type fakeDuplex struct {
	log    *callLog
	caller *io.PipeWriter
	mu     sync.Mutex
	played bytes.Buffer
}

func (d *fakeDuplex) Open(context.Context) (io.ReadCloser, io.WriteCloser, error) {
	d.log.add("duplex open")
	r, w := io.Pipe()
	d.caller = w
	return r, playSink{d}, nil
}

type playSink struct{ d *fakeDuplex }

func (p playSink) Write(b []byte) (int, error) {
	p.d.mu.Lock()
	defer p.d.mu.Unlock()
	return p.d.played.Write(b)
}
func (p playSink) Close() error { p.d.log.add("duplex close"); return nil }

func (d *fakeDuplex) playedBytes() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]byte(nil), d.played.Bytes()...)
}

// keeper is calld's side of what a call leaves behind: the settings it is read with (counted) and
// what each call kept.
type keeper struct {
	mu    sync.Mutex
	set   hostsettings.CallsKeep
	known bool
	reads int
	kept  map[string]Kept
}

func (k *keeper) settings() (hostsettings.CallsKeep, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.reads++
	return k.set, k.known
}

func (k *keeper) change(set hostsettings.CallsKeep) {
	k.mu.Lock()
	k.set = set
	k.mu.Unlock()
}

func (k *keeper) keep(path string, got Kept) {
	k.mu.Lock()
	k.kept[path] = got
	k.mu.Unlock()
}

// converser is a Caller on fakes; hostd has answered with both switches off.
func converser(t *testing.T, script ...ModemCall) (*Caller, *dialModem, *fakeDuplex, *keeper) {
	t.Helper()
	log := &callLog{}
	m := &dialModem{log: log, script: script}
	d := &fakeDuplex{log: log}
	k := &keeper{known: true, kept: map[string]Kept{}}
	c := NewCaller(m, &fakeAudio{log: log}, d, func(string) func() { return func() {} }, k.keep, k.settings, t.TempDir(), 50*time.Millisecond, 2*time.Millisecond)
	return c, m, d, k
}

func opener(t *fakeTalker) func(context.Context) (Talker, error) {
	return func(context.Context) (Talker, error) { return t, nil }
}

// waitScript waits for the call's context to end and says so.
func waitScript(ctx context.Context, _ Talker) (string, string) {
	<-ctx.Done()
	return "failed", ""
}

func TestConverseOpensBeforeDialling(t *testing.T) {
	c, m, _, _ := converser(t, st("active", "accepted"))
	_, err := c.Converse(ctx, "+351912345678", func(context.Context) (Talker, error) { return nil, realtime.ErrRefused }, waitScript, held)
	if !errors.Is(err, realtime.ErrRefused) {
		t.Fatal(err)
	}
	if got := m.log.get(); len(got) != 0 {
		t.Fatalf("dialled anyway: %v", got)
	}
}

func TestConversePumpsAudioBothWays(t *testing.T) {
	c, _, d, _ := converser(t, st("active", "accepted"))
	tk := newTalker(nil)
	voice := s16(1000, -1000, 2000, -2000)
	model := toULaw(s16(500, -500))
	out, err := c.Converse(ctx, "+351912345678", opener(tk), func(ctx context.Context, _ Talker) (string, string) {
		go d.caller.Write(voice)
		tk.audio <- model
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
			tk.mu.Lock()
			in := len(tk.appended)
			tk.mu.Unlock()
			if in >= 4 && len(d.playedBytes()) >= 4 {
				return "passed", ""
			}
		}
		return "failed", ""
	}, held)
	if err != nil || out != "passed" {
		t.Fatalf("%q %v", out, err)
	}
	if !bytes.Equal(tk.appended, toULaw(voice)) {
		t.Fatalf("model got %v, want %v", tk.appended, toULaw(voice))
	}
	if !bytes.Equal(d.playedBytes(), fromULaw(model)) {
		t.Fatalf("caller got %v", d.playedBytes())
	}
}

func TestConverseAlwaysUnroutesAndHangsUp(t *testing.T) {
	for _, outcome := range []string{"passed", "wrong", "failed"} {
		c, m, _, _ := converser(t, st("active", "accepted"))
		tk := newTalker(nil)
		out, err := c.Converse(ctx, "+351912345678", opener(tk), func(context.Context, Talker) (string, string) { return outcome, "" }, held)
		if err != nil || out != outcome {
			t.Fatalf("%s: %q %v", outcome, out, err)
		}
		want := []string{"dial +351912345678", "route", "duplex open", "duplex close", "unroute", "hangup /Call/1"}
		if got := m.log.get(); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %v", outcome, got)
		}
		if !tk.closed {
			t.Fatalf("%s: session left open", outcome)
		}
	}
}

func TestConverseUnansweredNeverRoutes(t *testing.T) {
	c, m, _, _ := converser(t, st("ringing-out", ""), st("terminated", "refused-or-busy"))
	tk := newTalker(nil)
	out, err := c.Converse(ctx, "+351912345678", opener(tk), waitScript, held)
	if err != nil || out != "busy" {
		t.Fatalf("%q %v", out, err)
	}
	if got := m.log.get(); !reflect.DeepEqual(got, []string{"dial +351912345678", "hangup /Call/1"}) {
		t.Fatalf("%v", got)
	}
	if !tk.closed {
		t.Fatal("session left open")
	}
}

func within(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("still running after %v", d)
	}
}

func TestConverseEndsWhenTheCallEnds(t *testing.T) {
	c, m, _, _ := converser(t, st("active", "accepted"), st("active", ""), st("active", ""), st("terminated", "terminated"))
	within(t, time.Second, func() {
		out, _ := c.Converse(ctx, "+351912345678", opener(newTalker(nil)), waitScript, held)
		if out != "failed" {
			t.Errorf("%q", out)
		}
	})
	if got := m.log.get(); got[len(got)-1] != "hangup /Call/1" {
		t.Fatalf("%v", got)
	}
}

func TestConverseEndsWhenTheSessionDrops(t *testing.T) {
	c, m, _, _ := converser(t, st("active", "accepted"))
	tk := newTalker(nil)
	go func() { time.Sleep(20 * time.Millisecond); tk.drop() }()
	within(t, time.Second, func() {
		if out, _ := c.Converse(ctx, "+351912345678", opener(tk), waitScript, held); out != "failed" {
			t.Errorf("%q", out)
		}
	})
	if got := m.log.get(); got[len(got)-1] != "hangup /Call/1" {
		t.Fatalf("%v", got)
	}
}

// stuckTalker's Append blocks until it is closed: a socket whose peer stopped reading.
type stuckTalker struct {
	*fakeTalker
	freed chan struct{}
	once  sync.Once
}

func (s *stuckTalker) Append([]byte) error { <-s.freed; return errors.New("closed") }
func (s *stuckTalker) Close() error {
	s.once.Do(func() { close(s.freed) })
	return s.fakeTalker.Close()
}

// A stalled session must never hold the call: past the cap, it is still unrouted and hung up.
func TestConverseHangsUpWhenAppendSticks(t *testing.T) {
	c, m, d, _ := converser(t, st("active", "accepted"))
	tk := &stuckTalker{fakeTalker: newTalker(nil), freed: make(chan struct{})}
	within(t, time.Second, func() {
		c.Converse(ctx, "+351912345678", func(context.Context) (Talker, error) { return tk, nil }, func(ctx context.Context, _ Talker) (string, string) {
			go d.caller.Write(make([]byte, 640)) // the caller speaks; the session takes none of it
			<-ctx.Done()
			return "failed", ""
		}, Held{Limit: 50 * time.Millisecond})
	})
	if got := m.log.get(); got[len(got)-1] != "hangup /Call/1" || got[len(got)-2] != "unroute" {
		t.Fatalf("%v", got)
	}
}

// The model's last words are generated faster than they play: the box hangs up only once all of
// them — still queued or still playing — have reached the caller.
func TestConverseLetsTheLastWordsPlay(t *testing.T) {
	c, m, d, _ := converser(t, st("active", "accepted"))
	tk := newTalker(nil)
	words := make([]byte, 800) // 100 ms of μ-law
	start := time.Now()
	c.Converse(ctx, "+351912345678", opener(tk), func(context.Context, Talker) (string, string) {
		tk.audio <- words[:400]
		tk.audio <- words[400:] // queued, not yet written, when the script ends
		return "passed", ""
	}, held)
	if took := time.Since(start); took < 90*time.Millisecond {
		t.Fatalf("hung up after %v, before 100 ms of goodbye played", took)
	}
	if got := len(d.playedBytes()); got != 2*len(words) {
		t.Fatalf("played %d of %d bytes", got, 2*len(words))
	}
	if got := m.log.get(); got[len(got)-1] != "hangup /Call/1" {
		t.Fatalf("%v", got)
	}
}

// The session hands its queued voice over as it is taken (realtime): a chunk that reaches the
// player while the last words still play is played too, before the hangup.
func TestConversePlaysWhatArrivesWhileTheLastWordsPlay(t *testing.T) {
	c, _, d, _ := converser(t, st("active", "accepted"))
	tk := newTalker(nil)
	words, late := make([]byte, 800), make([]byte, 80) // 100 ms of μ-law, then 10 ms more
	c.Converse(ctx, "+351912345678", opener(tk), func(context.Context, Talker) (string, string) {
		tk.audio <- words
		go func() {
			time.Sleep(20 * time.Millisecond)
			tk.audio <- late
		}()
		return "passed", ""
	}, held)
	if got := len(d.playedBytes()); got != 2*(len(words)+len(late)) {
		t.Fatalf("played %d of %d bytes", got, 2*(len(words)+len(late)))
	}
}

// replying is a session the model leads: whatever the other side says gets a reply.
type replying struct {
	*fakeTalker
	reply []byte
}

func (r *replying) Append(b []byte) error {
	r.fakeTalker.Append(b)
	select {
	case r.audio <- r.reply:
	default:
	}
	return nil
}

// Once the script is done the outcome is decided: what the other side says after it reaches no
// session, so no new reply starts, and the box hangs up once its last words have played.
func TestNothingReachesTheSessionOnceTheScriptIsDone(t *testing.T) {
	c, _, d, _ := converser(t, st("active", "accepted"))
	tk := &replying{fakeTalker: newTalker(nil), reply: make([]byte, 80)}
	after := s16(-3000, -3000) // the other side, still talking
	within(t, 2*time.Second, func() {
		c.Converse(ctx, "+351912345678", func(context.Context) (Talker, error) { return tk, nil }, func(context.Context, Talker) (string, string) {
			tk.audio <- make([]byte, 800) // the goodbye: 100 ms
			go func() {
				time.Sleep(20 * time.Millisecond)
				for {
					if _, err := d.caller.Write(after); err != nil {
						return // the call is over
					}
				}
			}()
			return "done", ""
		}, held)
	})
	tk.mu.Lock()
	defer tk.mu.Unlock()
	if bytes.Contains(tk.appended, toULaw(after)) {
		t.Fatal("the other side's words reached the session after the script was done")
	}
}

func TestConverseCapsTheCall(t *testing.T) {
	c, _, _, _ := converser(t, st("active", "accepted"))
	within(t, time.Second, func() {
		if out, _ := c.Converse(ctx, "+351912345678", opener(newTalker(nil)), waitScript, Held{Limit: 50 * time.Millisecond}); out != "failed" {
			t.Errorf("%q", out)
		}
	})
}

func TestBothOffKeepsOnlyTheSummary(t *testing.T) {
	c, _, _, k := converser(t, st("active", "accepted"))
	tk := newTalker(nil)
	c.Converse(ctx, "+351912345678", opener(tk), func(_ context.Context, got Talker) (string, string) {
		if got != Talker(tk) {
			t.Errorf("the script got a wrapper with both switches off")
		}
		tk.spoke <- "Hello"
		tk.heard <- "forty two"
		return "passed", "Asked 23 + 19 in English; heard \"forty two\"; passed."
	}, held)
	want := map[string]Kept{"/Call/1": {Summary: "Asked 23 + 19 in English; heard \"forty two\"; passed.", Result: "passed"}}
	if !reflect.DeepEqual(k.kept, want) {
		t.Fatalf("%+v", k.kept)
	}
	if left, _ := os.ReadDir(c.tmp); len(left) != 0 {
		t.Fatalf("files in tmp: %v", left)
	}
}

// Until hostd first answers nothing extra is kept, whatever hostd may have stored: never on a guess.
func TestNothingExtraIsKeptBeforeHostdAnswers(t *testing.T) {
	c, _, _, k := converser(t, st("active", "accepted"))
	k.known, k.set = false, hostsettings.CallsKeep{Transcript: true, Audio: true, DebugDays: 30}
	tk := newTalker(nil)
	c.Converse(ctx, "+351912345678", opener(tk), func(_ context.Context, got Talker) (string, string) {
		if got != Talker(tk) {
			t.Errorf("the script got a wrapper before hostd answered")
		}
		tk.spoke <- "Hello"
		tk.heard <- "forty two"
		return "passed", "summary"
	}, held)
	if want := map[string]Kept{"/Call/1": {Summary: "summary", Result: "passed"}}; !reflect.DeepEqual(k.kept, want) {
		t.Fatalf("%+v", k.kept)
	}
	if left, _ := os.ReadDir(c.tmp); len(left) != 0 {
		t.Fatalf("files in tmp: %v", left)
	}
}

func TestAnUnansweredCallKeepsNothingExtra(t *testing.T) {
	c, _, _, k := converser(t, st("ringing-out", ""), st("terminated", "refused-or-busy"))
	k.set = hostsettings.CallsKeep{Transcript: true, Audio: true, DebugDays: 30}
	if out, _ := c.Converse(ctx, "+351912345678", opener(newTalker(nil)), waitScript, held); out != "busy" {
		t.Fatal(out)
	}
	if len(k.kept) != 0 {
		t.Fatalf("%+v", k.kept)
	}
	if left, _ := os.ReadDir(c.tmp); len(left) != 0 {
		t.Fatalf("files in tmp: %v", left)
	}
}

func TestTranscriptOnKeepsBothSidesInOrder(t *testing.T) {
	c, _, _, k := converser(t, st("active", "accepted"))
	k.set = hostsettings.CallsKeep{Transcript: true, DebugDays: 30}
	c.now = func() time.Time { return t0 }
	tk := newTalker(nil)
	c.Converse(ctx, "+351912345678", opener(tk), func(ctx context.Context, w Talker) (string, string) {
		tk.spoke <- "Это ваш консьерж, проверяю линию. Сколько будет 23 плюс 19?"
		time.Sleep(20 * time.Millisecond) // the answer comes after the question
		tk.heard <- "сорок два"
		select {
		case got := <-w.Heard(): // forwarded to the script as before
			if got != "сорок два" {
				t.Errorf("heard %q", got)
			}
		case <-ctx.Done():
			t.Error("the answer never reached the script")
		}
		return "passed", "summary"
	}, held)
	want := Kept{Summary: "summary", Result: "passed", Turns: []Turn{
		{Speaker: "box", Text: "Это ваш консьерж, проверяю линию. Сколько будет 23 плюс 19?", At: ts(t0)},
		{Speaker: "them", Text: "сорок два", At: ts(t0)},
	}}
	if got := k.kept["/Call/1"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestAudioOnWritesAWavFile(t *testing.T) {
	c, _, d, k := converser(t, st("active", "accepted"))
	k.set = hostsettings.CallsKeep{Audio: true, DebugDays: 30}
	tk := newTalker(nil)
	model := toULaw(s16(500, -500))
	voice := s16(1000, -1000, 2000, -2000)
	c.Converse(ctx, "+351912345678", opener(tk), func(ctx context.Context, _ Talker) (string, string) {
		tk.audio <- model // the box speaks first, then the caller answers
		for len(d.playedBytes()) < 4 && ctx.Err() == nil {
			time.Sleep(time.Millisecond)
		}
		go d.caller.Write(voice)
		for ctx.Err() == nil {
			tk.mu.Lock()
			in := len(tk.appended)
			tk.mu.Unlock()
			if in >= 4 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		return "passed", "summary"
	}, held)
	got := k.kept["/Call/1"]
	if got.Summary != "summary" || got.Turns != nil || filepath.Dir(got.Audio) != c.tmp {
		t.Fatalf("%+v", got)
	}
	info, err := os.Stat(got.Audio)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	wav, _ := os.ReadFile(got.Audio)
	box := d.playedBytes() // what reached the sound card, 2 samples
	want := append(wavHeader(16), []byte{
		voice[0], voice[1], box[0], box[1],
		voice[2], voice[3], box[2], box[3],
		voice[4], voice[5], 0, 0,
		voice[6], voice[7], 0, 0,
	}...)
	if !bytes.Equal(wav, want) {
		t.Fatalf("wav\n%v\nwant\n%v", wav, want)
	}
}

// A switch flipped during a call never splits it half-kept: the call keeps per the value it started with.
func TestTheSettingIsReadOnceWhenTheCallStarts(t *testing.T) {
	c, _, _, k := converser(t, st("active", "accepted"))
	k.set = hostsettings.CallsKeep{Transcript: true, DebugDays: 30}
	tk := newTalker(nil)
	c.Converse(ctx, "+351912345678", opener(tk), func(ctx context.Context, w Talker) (string, string) {
		k.change(hostsettings.CallsKeep{Audio: true, DebugDays: 30})
		tk.heard <- "forty two"
		select {
		case <-w.Heard():
		case <-ctx.Done():
		}
		return "passed", "summary"
	}, held)
	got := k.kept["/Call/1"]
	if len(got.Turns) != 1 || got.Turns[0].Text != "forty two" || got.Audio != "" {
		t.Fatalf("%+v", got)
	}
	if k.reads != 1 {
		t.Fatalf("settings read %d times", k.reads)
	}
}

func TestConverseIsOneCallAtATime(t *testing.T) {
	c, _, _, _ := converser(t, st("active", "accepted"))
	started := make(chan struct{})
	release := make(chan struct{})
	go c.Converse(ctx, "+351912345678", opener(newTalker(nil)), func(context.Context, Talker) (string, string) {
		close(started)
		<-release
		return "passed", ""
	}, held)
	<-started
	if _, err := c.Converse(ctx, "+351900000000", opener(newTalker(nil)), waitScript, held); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	close(release)
}

// The spoken check script, on its own.

var en, ru = Language{"en", "English"}, Language{"ru", "Русский"}

func runScript(t *testing.T, s Script, tk *fakeTalker) (string, string) {
	t.Helper()
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s(cctx, tk)
}

func TestSpokenCheckPasses(t *testing.T) {
	tk := newTalker(map[int]string{0: "forty-two"})
	out, summary := runScript(t, spokenCheck(verify.Challenge{A: 23, B: 19}, en, time.Second), tk)
	if out != "passed" || summary != `Asked 23 + 19 in English; heard "forty-two"; passed.` {
		t.Fatalf("%q %q", out, summary)
	}
	if n := len(tk.saidSoFar()); n != 2 { // ask, thanks
		t.Fatalf("%d responses: %v", n, tk.saidSoFar())
	}
}

func TestSpokenCheckOneRetry(t *testing.T) {
	tk := newTalker(map[int]string{0: "forty one", 1: "forty two"})
	out, summary := runScript(t, spokenCheck(verify.Challenge{A: 23, B: 19}, en, time.Second), tk)
	if out != "passed" || summary != `Asked 23 + 19 in English; heard "forty two"; passed.` { // the last answer
		t.Fatalf("%q %q", out, summary)
	}
	if n := len(tk.saidSoFar()); n != 3 { // ask, ask again, thanks
		t.Fatalf("%d responses", n)
	}
}

func TestSpokenCheckWrongTwiceIsWrong(t *testing.T) {
	tk := newTalker(map[int]string{0: "forty one", 1: "forty three"})
	if out, _ := runScript(t, spokenCheck(verify.Challenge{A: 23, B: 19}, en, time.Second), tk); out != "wrong" {
		t.Fatal(out)
	}
	if n := len(tk.saidSoFar()); n != 3 { // ask, ask again, goodbye
		t.Fatalf("%d responses", n)
	}
}

func TestSpokenCheckSilenceTwiceIsWrong(t *testing.T) {
	tk := newTalker(nil)
	out, summary := runScript(t, spokenCheck(verify.Challenge{A: 23, B: 19}, en, 20*time.Millisecond), tk)
	if out != "wrong" || summary != "Asked 23 + 19 in English; heard nothing; wrong." {
		t.Fatalf("%q %q", out, summary)
	}
}

func TestTheCheckSummarisesItself(t *testing.T) {
	tk := newTalker(map[int]string{0: "сорок два"})
	out, summary := runScript(t, spokenCheck(verify.Challenge{A: 23, B: 19}, ru, time.Second), tk)
	if out != "passed" || summary != `Asked 23 + 19 in Russian; heard "сорок два"; passed.` {
		t.Fatalf("%q %q", out, summary)
	}
}

func TestSpokenCheckAsksInTheChosenLanguage(t *testing.T) {
	tk := newTalker(map[int]string{0: "сорок два"})
	if out, _ := runScript(t, spokenCheck(verify.Challenge{A: 23, B: 19}, ru, time.Second), tk); out != "passed" {
		t.Fatal(out)
	}
	first := tk.saidSoFar()[0]
	for _, want := range []string{"Russian", "23", "19"} {
		if !strings.Contains(first, want) {
			t.Fatalf("no %q in %q", want, first)
		}
	}
}

func TestSpokenCheckEndsWhenTheSessionDrops(t *testing.T) {
	tk := newTalker(nil)
	tk.drop()
	if out, _ := runScript(t, spokenCheck(verify.Challenge{A: 23, B: 19}, en, time.Second), tk); out != "failed" {
		t.Fatal(out)
	}
}

// Every sentence of the check reaches the model already in the chosen language: given an English
// quote to "say only", the model repeated the goodbye in English on a Russian call (2026-10-28).
func TestSpokenCheckSpeaksOnlyTheChosenLanguage(t *testing.T) {
	english := []string{"This is your concierge", "What is", "Sorry", "That's right", "thank you", "Goodbye"}
	for _, lang := range Languages {
		if lang.Code == "en" {
			continue
		}
		passed := newTalker(map[int]string{0: "999"}) // wrong: ask again, then goodbye
		runScript(t, spokenCheck(verify.Challenge{A: 23, B: 19}, lang, 20*time.Millisecond), passed)
		right := newTalker(nil)
		right.answers = map[int]string{0: spokenAnswer(lang.Code)}
		runScript(t, spokenCheck(verify.Challenge{A: 23, B: 19}, lang, time.Second), right)
		said := append(passed.saidSoFar(), right.saidSoFar()...)
		if len(said) < 4 {
			t.Fatalf("%s: only %d turns: %v", lang.Code, len(said), said)
		}
		for _, instr := range said {
			for _, e := range english {
				if strings.Contains(instr, e) {
					t.Fatalf("%s: English %q in %q", lang.Code, e, instr)
				}
			}
		}
	}
}

func TestEveryLanguageHasTheCheckPhrases(t *testing.T) {
	for _, lang := range Languages {
		p, ok := checkPhrases[lang.Code]
		if !ok || p.ask == "" || p.again == "" || p.thanks == "" || p.goodbye == "" {
			t.Fatalf("%s: phrases %+v", lang.Code, p)
		}
	}
}

// spokenAnswer is "42" as the owner would say it in each language.
func spokenAnswer(code string) string {
	return map[string]string{"pt": "quarenta e dois", "ru": "сорок два", "es": "cuarenta y dos"}[code]
}

// An answered call keeps what its session used, per model, priced; the switches don't matter.
func TestAHeldCallKeepsItsUsage(t *testing.T) {
	c, _, _, k := converser(t, st("active", "accepted"))
	tk := newTalker(nil)
	tk.usage = realtime.Usage{Call: realtime.Tokens{Model: "gpt-realtime-2.1", AudioIn: 1000, AudioOut: 500},
		Transcription: realtime.Tokens{Model: "gpt-4o-transcribe", AudioIn: 100, TextOut: 8}}
	c.Converse(ctx, "+351912345678", opener(tk), func(context.Context, Talker) (string, string) { return "passed", "" }, held)
	got := k.kept["/Call/1"].Usage
	if got == nil || !reflect.DeepEqual(got, costed(tk.usage)) {
		t.Fatalf("%+v", k.kept)
	}
}

// held is the limit most tests give a call: far past what any of them takes.
var held = Held{Limit: time.Minute}

func TestAHeldCallKeepsItsResultAndPrompt(t *testing.T) {
	c, _, _, k := converser(t, st("active", "accepted"))
	c.Converse(ctx, "+351912345678", opener(newTalker(nil)), func(context.Context, Talker) (string, string) {
		return "done", "Booked."
	}, Held{Limit: time.Minute, Prompt: "Book a table"})
	want := map[string]Kept{"/Call/1": {Summary: "Booked.", Result: "done", Prompt: "Book a table"}}
	if !reflect.DeepEqual(k.kept, want) {
		t.Fatalf("%+v", k.kept)
	}
}
