package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWorker plays the Python worker's half of the socket: connect, say hello, then answer.
type fakeWorker struct {
	t    *testing.T
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
}

// fakeListener is the worker's socket: the fake listens, the engine dials.
type fakeListener struct {
	ln   net.Listener
	path string
}

func listenFake(t *testing.T, path string) *fakeListener {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return &fakeListener{ln: ln, path: path}
}

// dialFake takes the engine's next connection and says hello on it, as the worker does on accept.
func dialFake(t *testing.T, l *fakeListener, version int) *fakeWorker {
	t.Helper()
	l.ln.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second))
	conn, err := l.ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	f := &fakeWorker{t: t, conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}
	f.send(Message{"id": "hello", "type": "hello", "worker_version": "0.1.0", "contract_version": version})
	return f
}

func (f *fakeWorker) send(m Message) {
	f.t.Helper()
	if err := f.enc.Encode(m); err != nil {
		f.t.Fatalf("send: %v", err)
	}
}

func (f *fakeWorker) read() Message {
	f.t.Helper()
	var m Message
	if err := f.dec.Decode(&m); err != nil {
		f.t.Fatalf("read: %v", err)
	}
	return m
}

func socketPath(t *testing.T) string {
	t.Helper()
	// Not t.TempDir(): it embeds the test name, and macOS caps a socket path at 104 bytes.
	dir, err := os.MkdirTemp("", "cw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "w.sock")
}

func init() { redialEvery = 20 * time.Millisecond }

// serveKey listens as the worker and starts a Worker dialling it, its model key from key.
func serveKey(t *testing.T, key func(context.Context) (string, error)) (*Worker, *fakeListener) {
	t.Helper()
	l := listenFake(t, socketPath(t))
	w := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx, l.path, key); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return w, l
}

func serve(t *testing.T) (*Worker, *fakeListener) { return serveKey(t, nil) }

func waitConnected(t *testing.T, w *Worker) {
	t.Helper()
	// current(), not Status(): Status pings, and an unanswered ping would sit in the fake's queue
	// ahead of the request the test is about.
	deadline := time.Now().Add(2 * time.Second)
	for w.current() == nil {
		if time.Now().After(deadline) {
			t.Fatal("worker never connected")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHandshakeAnswersHelloWithReady(t *testing.T) {
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion)

	ready := f.read()
	if ready["type"] != "ready" || ready["id"] != "hello" {
		t.Fatalf("want ready for hello, got %v", ready)
	}
	if ready["contract_version"] != float64(ContractVersion) {
		t.Fatalf("ready carries no contract_version: %v", ready)
	}
	waitConnected(t, w)
}

func TestHealthRoundTrip(t *testing.T) {
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion)
	f.read() // ready
	waitConnected(t, w)

	go func() {
		req := f.read()
		f.send(Message{"id": req["id"], "type": "pong", "contract_version": ContractVersion,
			"uptime_ms": 1234, "contexts_open": 0})
	}()

	st := w.Status(context.Background())
	if !st.Connected || st.WorkerVersion != "0.1.0" || st.UptimeMS != 1234 {
		t.Fatalf("unexpected status %+v", st)
	}
}

func TestWorkerErrorReplyBecomesError(t *testing.T) {
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion)
	f.read()
	waitConnected(t, w)

	go func() {
		req := f.read()
		f.send(Message{"id": req["id"], "type": "error", "contract_version": ContractVersion,
			"code": "handler_failed", "message": "boom"})
	}()

	_, err := w.Request(context.Background(), "session_check", Message{"provider": "x"})
	var werr *WorkerError
	if !errors.As(err, &werr) || werr.Code != "handler_failed" {
		t.Fatalf("want WorkerError handler_failed, got %v", err)
	}
}

func TestIncompatibleContractVersionIsRefused(t *testing.T) {
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion+2)

	var m Message
	if err := f.dec.Decode(&m); err == nil {
		t.Fatalf("want the connection closed, got %v", m)
	}
	if w.Status(context.Background()).Connected {
		t.Fatal("an incompatible worker must not count as connected")
	}
}

func TestDisconnectFailsPendingRequest(t *testing.T) {
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion)
	f.read()
	waitConnected(t, w)

	go func() {
		f.read()
		f.conn.Close()
	}()

	if _, err := w.Request(context.Background(), "health", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable after hang-up, got %v", err)
	}
}

// tryRead is read for goroutines: it reports a failure instead of calling t.Fatalf off the test
// goroutine.
func (f *fakeWorker) tryRead() (Message, error) {
	var m Message
	err := f.dec.Decode(&m)
	return m, err
}

func askFrame(id any, askID string) Message {
	return Message{"id": id, "type": "value_request", "contract_version": ContractVersion,
		"ask_id": askID, "name": "sms_code", "why": "the site texted a code"}
}

// runFlowWithValueRequest drives one run_agent request whose worker asks for a value mid-flight and
// returns the value_reply the worker received.
func runFlowWithValueRequest(t *testing.T, ask Ask, extra Message) Message {
	t.Helper()
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion)
	f.read() // ready
	waitConnected(t, w)

	replied := make(chan Message, 1)
	go func() {
		defer close(replied)
		req, err := f.tryRead()
		if err != nil {
			t.Errorf("fake read request: %v", err)
			return
		}
		frame := askFrame(req["id"], "a1")
		maps.Copy(frame, extra)
		f.enc.Encode(frame)
		r, err := f.tryRead()
		if err != nil {
			t.Errorf("fake read value_reply: %v", err)
			return
		}
		replied <- r
		f.enc.Encode(Message{"id": req["id"], "type": "step_result", "contract_version": ContractVersion})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := w.RequestAsk(ctx, "run_agent", Message{}, ask, nil)
	if err != nil {
		t.Fatalf("RequestAsk: %v", err)
	}
	if res["type"] != "step_result" {
		t.Fatalf("want step_result, got %v", res)
	}
	return <-replied
}

// A run's step_progress frames reach its Progress in order, every one before the reply returns,
// and another request's frames never do.
func TestStepProgressReachesItsRequestInOrder(t *testing.T) {
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion)
	f.read() // ready
	waitConnected(t, w)
	go func() {
		req, err := f.tryRead()
		if err != nil {
			t.Errorf("fake read request: %v", err)
			return
		}
		progress := func(id any, event string, n int) Message {
			return Message{"id": id, "type": "step_progress", "contract_version": ContractVersion,
				"event": event, "payload": map[string]any{"step": n}}
		}
		f.enc.Encode(progress("someone-else", "goal", 0))
		for n := 1; n <= 50; n++ {
			f.enc.Encode(progress(req["id"], "goal", n))
			f.enc.Encode(progress(req["id"], "action", n))
		}
		f.enc.Encode(Message{"id": req["id"], "type": "step_result", "contract_version": ContractVersion})
	}()
	var got []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := w.RequestAsk(ctx, "run_agent", Message{}, nil, func(event string, payload Message) {
		got = append(got, fmt.Sprintf("%s %v", event, payload["step"]))
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 || got[0] != "goal 1" || got[1] != "action 1" || got[99] != "action 50" {
		t.Fatalf("progress = %d lines: %v", len(got), got)
	}
}

func TestAValueRequestIsAnsweredByAsk(t *testing.T) {
	var gotName, gotWhy string
	reply := runFlowWithValueRequest(t, func(_ context.Context, q AskRequest) (string, bool) {
		gotName, gotWhy = q.Name, q.Why
		return "6828", true
	}, nil)
	if reply["type"] != "value_reply" || reply["ask_id"] != "a1" || reply["value"] != "6828" {
		t.Fatalf("want value_reply a1 6828, got %v", reply)
	}
	if gotName != "sms_code" || gotWhy != "the site texted a code" {
		t.Fatalf("ask got name=%q why=%q", gotName, gotWhy)
	}
}

func TestAnAskCarriesItsOptions(t *testing.T) {
	var got [][]string
	ask := func(_ context.Context, q AskRequest) (string, bool) {
		got = append(got, q.Options)
		return "Sat", true
	}
	runFlowWithValueRequest(t, ask, Message{"name": "question", "options": []any{"Sat", "Sun"}})
	runFlowWithValueRequest(t, ask, nil)
	if len(got) != 2 || !slices.Equal(got[0], []string{"Sat", "Sun"}) || got[1] != nil {
		t.Fatalf("options = %v", got)
	}
}

func TestAValueRequestWithoutAskIsAnsweredNone(t *testing.T) {
	reply := runFlowWithValueRequest(t, nil, nil)
	v, present := reply["value"]
	if reply["type"] != "value_reply" || reply["ask_id"] != "a1" || !present || v != nil {
		t.Fatalf("want value_reply a1 with null value, got %v", reply)
	}
}

func TestAskDeclinedIsAnsweredNone(t *testing.T) {
	reply := runFlowWithValueRequest(t, func(context.Context, AskRequest) (string, bool) { return "x", false }, nil)
	if v, present := reply["value"]; !present || v != nil {
		t.Fatalf("want null value, got %v", reply)
	}
}

// 20: the model requests carry the OpenRouter key.
func TestContractVersion20(t *testing.T) {
	if ContractVersion != 20 {
		t.Fatalf("ContractVersion = %d, want 20", ContractVersion)
	}
	for _, v := range []int{19, 21} {
		w, ln := serve(t)
		f := dialFake(t, ln, v)
		if ready := f.read(); ready["type"] != "ready" {
			t.Fatalf("a v%d worker must be accepted, got %v", v, ready)
		}
		waitConnected(t, w)
	}
	for _, v := range []int{18, 22} {
		w, ln := serve(t)
		f := dialFake(t, ln, v)
		var m Message
		if err := f.dec.Decode(&m); err == nil {
			t.Fatalf("a v%d worker must be refused, got %v", v, m)
		}
		if w.Status(context.Background()).Connected {
			t.Fatalf("a v%d worker must not count as connected", v)
		}
	}
}

const sentinelKey = "sk-or-test-SENTINEL"

// The three requests that run a model carry the owner's key; the others never do.
func TestModelRequestsCarryTheKey(t *testing.T) {
	w, ln := serveKey(t, func(context.Context) (string, error) { return sentinelKey, nil })
	f := dialFake(t, ln, ContractVersion)
	f.read() // ready
	waitConnected(t, w)
	for typ, carries := range map[string]bool{"site_card": true, "read_arranged": true, "run_agent": true, "health": false, "read_signed_in": false} {
		go func() {
			req := f.read()
			f.send(Message{"id": req["id"], "type": "reply", "contract_version": ContractVersion, "saw": req["openrouter_key"]})
		}()
		reply, err := w.Request(context.Background(), typ, Message{})
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if carries && reply["saw"] != sentinelKey {
			t.Errorf("%s must carry the key, saw %v", typ, reply["saw"])
		}
		if !carries && reply["saw"] != nil {
			t.Errorf("%s must not carry the key, saw %v", typ, reply["saw"])
		}
	}
}

// A key that can't be had fails the request before anything is sent; an empty one is sent as it is.
func TestKeyErrorFailsTheRequestBeforeItIsSent(t *testing.T) {
	boom := errors.New("secrets unavailable")
	var failing atomic.Bool
	failing.Store(true)
	w, ln := serveKey(t, func(context.Context) (string, error) {
		if failing.Load() {
			return "", boom
		}
		return "", nil
	})
	f := dialFake(t, ln, ContractVersion)
	f.read()
	waitConnected(t, w)
	if _, err := w.Request(context.Background(), "run_agent", Message{}); !errors.Is(err, boom) {
		t.Fatalf("want the key's error, got %v", err)
	}
	failing.Store(false)
	go func() {
		req := f.read()
		f.send(Message{"id": req["id"], "type": "reply", "contract_version": ContractVersion, "saw": req["openrouter_key"]})
	}()
	reply, err := w.Request(context.Background(), "run_agent", Message{})
	if err != nil || reply["saw"] != "" {
		t.Fatalf("an empty key travels as an empty string: %v %v", reply, err)
	}
}

// lockedBuffer is a log sink the serving goroutines and the test may touch at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The engine logs nothing that carries the key.
func TestKeyNeverLogged(t *testing.T) {
	var buf lockedBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	w, ln := serveKey(t, func(context.Context) (string, error) { return sentinelKey, nil })
	f := dialFake(t, ln, ContractVersion)
	f.read()
	waitConnected(t, w)
	go func() {
		req := f.read()
		f.send(Message{"id": req["id"], "type": "reply", "contract_version": ContractVersion})
	}()
	if _, err := w.Request(context.Background(), "run_agent", Message{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), sentinelKey) {
		t.Fatalf("the key is in the log: %s", buf.String())
	}
}

func TestValueRequestsInOneRequestAreAnsweredInOrder(t *testing.T) {
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion)
	f.read()
	waitConnected(t, w)

	got := make(chan []string, 1)
	go func() {
		req, err := f.tryRead()
		if err != nil {
			t.Errorf("fake read: %v", err)
			return
		}
		var values []string
		for _, id := range []string{"a1", "a2"} {
			f.enc.Encode(askFrame(req["id"], id))
			r, err := f.tryRead()
			if err != nil {
				t.Errorf("fake read reply: %v", err)
				return
			}
			values = append(values, r["ask_id"].(string)+"="+r["value"].(string))
		}
		got <- values
		f.enc.Encode(Message{"id": req["id"], "type": "step_result", "contract_version": ContractVersion})
	}()

	n := 0
	ask := func(context.Context, AskRequest) (string, bool) { n++; return fmt.Sprint("code", n), true }
	if _, err := w.RequestAsk(context.Background(), "run_agent", nil, ask, nil); err != nil {
		t.Fatalf("RequestAsk: %v", err)
	}
	if v := <-got; len(v) != 2 || v[0] != "a1=code1" || v[1] != "a2=code2" {
		t.Fatalf("want a1=code1, a2=code2 in order, got %v", v)
	}
}

// askInFlight starts a RequestAsk whose ask blocks until its ctx ends, once the worker has asked.
func askInFlight(t *testing.T, ctx context.Context) (f *fakeWorker, asking <-chan struct{}, askCtxDone <-chan struct{}, result <-chan error) {
	t.Helper()
	w, ln := serve(t)
	f = dialFake(t, ln, ContractVersion)
	f.read()
	waitConnected(t, w)
	go func() {
		req, err := f.tryRead()
		if err != nil {
			return
		}
		f.enc.Encode(askFrame(req["id"], "a1"))
	}()
	started, done, res := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	ask := func(c context.Context, _ AskRequest) (string, bool) {
		close(started)
		<-c.Done()
		close(done)
		return "", false
	}
	go func() {
		_, err := w.RequestAsk(ctx, "run_agent", nil, ask, nil)
		res <- err
	}()
	return f, started, done, res
}

func TestCallerCancelWhileAskRunsReturnsCtxErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, asking, _, result := askInFlight(t, ctx)
	<-asking
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RequestAsk hung after the caller cancelled")
	}
}

// A request its caller gave up on tells the worker, or its run would go on with nobody listening, holding
// its site's browser. It says so by the request's own id, whether it was asking or working.
func TestACallerWhoGivesUpTellsTheWorker(t *testing.T) {
	t.Run("while it works", func(t *testing.T) {
		w, ln := serve(t)
		f := dialFake(t, ln, ContractVersion)
		f.read()
		waitConnected(t, w)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := w.RequestAsk(ctx, "run_agent", nil, nil, nil)
			result <- err
		}()
		req := f.read()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if m := f.read(); m["type"] != "cancel" || m["id"] != req["id"] {
			t.Fatalf("frame = %v, want a cancel of %v", m, req["id"])
		}
	})
	t.Run("while it asks", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f, asking, _, result := askInFlight(t, ctx)
		<-asking
		cancel()
		<-result
		if m := f.read(); m["type"] != "cancel" {
			t.Fatalf("frame = %v", m)
		}
	})
	t.Run("not after its reply", func(t *testing.T) {
		w, ln := serve(t)
		f := dialFake(t, ln, ContractVersion)
		f.read()
		waitConnected(t, w)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			req := f.read()
			f.send(Message{"id": req["id"], "type": "step_result", "contract_version": ContractVersion})
		}()
		if _, err := w.RequestAsk(ctx, "run_agent", nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		cancel()
		f.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		if m, err := f.tryRead(); err == nil {
			t.Fatalf("frame = %v", m)
		}
	})
}

// What the worker reported before a caller gave up still reaches the caller: a press look's frame decides
// whether the site's email is asked.
func TestProgressQueuedWhenACallerGivesUpIsNotLost(t *testing.T) {
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion)
	f.read()
	waitConnected(t, w)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, holding := make(chan struct{}), make(chan struct{})
	ask := func(context.Context, AskRequest) (string, bool) { // the frame queues while this holds
		close(holding)
		<-release
		return "", false
	}
	var got []string
	result := make(chan error, 1)
	go func() {
		_, err := w.RequestAsk(ctx, "run_agent", nil, ask, func(event string, _ Message) { got = append(got, event) })
		result <- err
	}()
	req := f.read()
	f.send(askFrame(req["id"], "a1"))
	// Only once the ask holds: sent together, the ask and the frame race in RequestAsk's select, and a
	// frame taken first is delivered before anything holds (1 run in ~5 failed so).
	<-holding
	f.send(Message{"id": req["id"], "type": "step_progress", "contract_version": ContractVersion, "event": "action", "payload": map[string]any{}})
	queued := func() bool {
		s := w.current()
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, p := range s.pending {
			if len(p.progress) == 1 {
				return true
			}
		}
		return false
	}
	for deadline := time.Now().Add(2 * time.Second); !queued(); {
		if time.Now().After(deadline) {
			t.Fatal("the frame never queued")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("progress = %v", got)
	}
}

func TestWorkerHangupWhileAskRunsCancelsAskAndFails(t *testing.T) {
	f, asking, askDone, result := askInFlight(t, context.Background())
	<-asking
	f.conn.Close()
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("want ErrUnavailable, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RequestAsk hung after the worker hung up")
	}
	select {
	case <-askDone:
	default:
		t.Fatal("ask's ctx was not cancelled")
	}
}

func TestSecondUnansweredValueRequestFailsTheRequest(t *testing.T) {
	s := &session{pending: map[string]*pendingReq{}, done: make(chan struct{})}
	p, _ := s.register("e1")
	s.deliver(askFrame("e1", "a1"))
	s.deliver(askFrame("e1", "a2"))
	if m := <-p.reply; m["type"] != "error" {
		t.Fatalf("want an error reply, got %v", m)
	}
}

// A frame no request asked for (a v2 worker's live_frame) goes nowhere, and the connection goes on
// working.
func TestAFrameNoRequestAskedForIsDropped(t *testing.T) {
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion)
	f.read()
	waitConnected(t, w)
	go func() {
		req, err := f.tryRead()
		if err != nil {
			return
		}
		f.enc.Encode(Message{"type": "live_frame", "site": "s", "jpeg": "AA==", "contract_version": ContractVersion})
		f.enc.Encode(Message{"id": req["id"], "type": "health", "contract_version": ContractVersion})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.Request(ctx, "health", nil); err != nil {
		t.Fatalf("Request: %v", err)
	}
}

func TestRunConnectsToAListeningWorker(t *testing.T) {
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion)
	f.read() // ready
	waitConnected(t, w)
	go func() {
		for {
			req, err := f.tryRead()
			if err != nil {
				return
			}
			f.send(Message{"id": req["id"], "type": "pong", "contract_version": ContractVersion})
		}
	}()
	if st := w.Status(context.Background()); !st.Connected || st.Error != "" {
		t.Fatalf("want connected, got %+v", st)
	}
	if m, err := w.Request(context.Background(), "health", nil); err != nil || m["type"] != "pong" {
		t.Fatalf("want pong, got %v %v", m, err)
	}
}

func TestRunRedialsAfterTheWorkerGoes(t *testing.T) {
	w, ln := serve(t)
	f := dialFake(t, ln, ContractVersion)
	f.read()
	waitConnected(t, w)

	go func() {
		f.read()
		f.conn.Close()
	}()
	if _, err := w.Request(context.Background(), "health", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a request in flight at the hang-up must be unavailable, got %v", err)
	}
	f2 := dialFake(t, ln, ContractVersion)
	if ready := f2.read(); ready["type"] != "ready" {
		t.Fatalf("want ready on the re-dial, got %v", ready)
	}
	waitConnected(t, w)
	go func() {
		req := f2.read()
		f2.send(Message{"id": req["id"], "type": "pong", "contract_version": ContractVersion})
	}()
	if _, err := w.Request(context.Background(), "health", nil); err != nil {
		t.Fatalf("after the re-dial: %v", err)
	}
}

func TestRunWaitsForAWorkerNotYetListening(t *testing.T) {
	path := socketPath(t)
	w := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx, path, nil); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	time.Sleep(100 * time.Millisecond) // several failed dials
	if w.current() != nil {
		t.Fatal("connected to nothing")
	}
	l := listenFake(t, path)
	f := dialFake(t, l, ContractVersion)
	f.read()
	waitConnected(t, w)
}

func TestRunReturnsWhenItsContextEnds(t *testing.T) {
	l := listenFake(t, socketPath(t))
	w := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx, l.path, nil); close(done) }()
	f := dialFake(t, l, ContractVersion)
	f.read()
	waitConnected(t, w)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
	if w.current() != nil {
		t.Fatal("still connected after Run returned")
	}
}

// A worker that can't be reached is said once, not at every redial; reaching it again is said too.
func TestRunSaysOnceWhyTheWorkerCannotBeReached(t *testing.T) {
	var buf lockedBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	path := socketPath(t)
	w := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx, path, nil); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	time.Sleep(200 * time.Millisecond) // about ten failed dials
	if n := strings.Count(buf.String(), "browser worker unreachable"); n != 1 {
		t.Fatalf("want one line for a repeated failure, got %d:\n%s", n, buf.String())
	}
	l := listenFake(t, path)
	f := dialFake(t, l, ContractVersion)
	f.read()
	waitConnected(t, w)
	if !strings.Contains(buf.String(), "browser worker connected") {
		t.Fatalf("want the connection said:\n%s", buf.String())
	}
}

func TestRequestWithNoWorkerIsUnavailable(t *testing.T) {
	w := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx, socketPath(t), nil); close(done) }() // nothing listens
	defer func() { cancel(); <-done }()
	if _, err := w.Request(context.Background(), "health", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}
