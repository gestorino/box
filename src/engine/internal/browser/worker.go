// Package browser is the engine's side of the browser-use worker (src/browser/).
//
// The contract is the one the Python worker speaks (src/browser/concierge_worker/ipc.py):
// newline-delimited JSON over a Unix socket the WORKER binds. The engine dials it; the worker sends
// `hello` on accept; the engine answers `ready`; after that every request carries an `id` and the
// worker's reply carries the same one. A worker whose contract_version is more than one away is refused.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ContractVersion is the IPC contract this engine speaks; a worker within ±1 of it is accepted.
// v2 adds value_request / value_reply: a long request may ask the engine for a value mid-flight,
// such as an SMS code. v3 drops the screencast's live_frame / live_input / live_result
// (each run draws on its own screen) and run_flow / run_check's watch. v4 drops the `shot` progress
// event for the run's recording: run_flow / run_check read record_to / record_max_bytes and report
// `recording`, `recording_failed` and `screen_done`, and an `action` may carry x, y. v5 and v6 were
// the bookings' (run_errand, site_actions); v6 also made cancel under a request's id give that request
// up (RequestAsk), and nothing answers it. v7: run_errand and site_actions removed. v8: run_errand
// (an errand), and a value_request may carry `options`, the choices of an errand's question. v9: an
// errand tells each model call as a `model_call` step_progress, and its reply carries no cost. v10: only
// errands run in the browser — execute_step, session_check, account_check, run_flow and run_check
// removed; site_card reads a page's card; run_errand carries the owner's `values`; a value_request may
// be named sms_code or email. v11: read_signed_in reads a saved page without the model
// (read_signed_in_result: result, found, url), and run_errand's reply tells its final_url. v12: a
// footprint is a link and the clicks after it: run_errand takes prove (proofs, old) and answers
// footprint (url, clicks, or null) in place of final_url; read_signed_in takes clicks and may answer
// human_check. v13: run_errand names its kind (errand, register, check), which picks the worker's
// prompt: only an errand asks the owner to confirm its final press. v14: run_errand is run_agent
// (agent.py): it runs every kind, not only errands. v15: site_card suggests the site's actions
// (actions, actions_error) and tells its model call as a model_call step_progress.
// v16: site_card's actions are {text, params}.
// v17: run_agent's record → appointment(s); a site_card action's effect.
// v18: read_arranged reads what a finished run arranged out of its words.
// v19: site_card, read_arranged and run_agent carry `openrouter_key`, the owner's key as the engine has
// it now; the worker no longer reads a key file.
// v20: the worker listens on its own socket and the engine dials it (hello still comes first, from the
// worker); the worker owns its profiles, screens and recordings directories.
const ContractVersion = 20

const handshakeTimeout = 10 * time.Second

// redialEvery is how long Run waits after a failed dial or a lost connection. A worker that dropped
// the engine may refuse it for a while as its old runs unwind, so Run just keeps trying.
var redialEvery = time.Second

// Message is one frame on the socket.
type Message map[string]any

// ErrUnavailable means no worker is connected, or it hung up before answering.
var ErrUnavailable = errors.New("browser worker unavailable")

// WorkerError is the worker's own `error` reply.
type WorkerError struct{ Code, Message string }

func (e *WorkerError) Error() string { return fmt.Sprintf("browser worker: %s: %s", e.Code, e.Message) }

// Status is what the API reports about the worker.
type Status struct {
	Connected     bool   `json:"connected"`
	WorkerVersion string `json:"worker_version,omitempty"`
	UptimeMS      int64  `json:"uptime_ms,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Worker holds the current connection to the browser-use worker. A worker restart is a re-dial.
type Worker struct {
	mu  sync.Mutex
	cur *session
	seq atomic.Uint64
	key func(ctx context.Context) (string, error) // set by Run before its first dial; nil sends ""
}

// modelRequests are the request types that run a model: each frame carries the OpenRouter key.
var modelRequests = map[string]bool{"site_card": true, "read_arranged": true, "run_agent": true}

func New() *Worker { return &Worker{} }

// Run dials the worker's socket and serves the connection until it drops, then dials again every
// redialEvery, until ctx ends. key gives the owner's OpenRouter key as it is now ("" when none is set);
// each request that runs a model carries it in its frame.
func (w *Worker) Run(ctx context.Context, socket string, key func(context.Context) (string, error)) {
	w.key = key
	var d net.Dialer
	last := "" // why the worker could not be reached, said once while it stays the same
	for {
		conn, err := d.DialContext(ctx, "unix", socket)
		if err == nil {
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			var connected bool
			connected, err = w.handle(conn)
			stop()
			if connected {
				last = ""
			}
		}
		if err != nil && ctx.Err() == nil && err.Error() != last {
			last = err.Error()
			slog.Warn("browser worker unreachable", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(redialEvery):
		}
	}
}

// AskRequest is what the worker asked for: a name (`sms_code`, `human_check`, `question`), why, and
// for an errand's question the choices the page offers.
type AskRequest struct {
	Name, Why string
	Options   []string
}

// Ask supplies a value the worker asked for mid-request (an SMS code, say). It may block for
// minutes; ctx is the request's. ok == false means no value is available.
type Ask func(ctx context.Context, q AskRequest) (value string, ok bool)

// Progress receives a request's `step_progress` frames, in the order the worker sent them: the
// event (`action`, `goal`, `trace_error` …) and its payload. It runs on the request's goroutine.
type Progress func(event string, payload Message)

// progressBuffer is how many step_progress frames may wait for their request's goroutine; past it a
// frame is dropped rather than stall the socket every other request shares.
const progressBuffer = 512

// Request sends one message and waits for the reply with the same id.
func (w *Worker) Request(ctx context.Context, typ string, body Message) (Message, error) {
	return w.RequestAsk(ctx, typ, body, nil, nil)
}

// RequestAsk is Request for a worker that may send `value_request` and `step_progress` frames before
// it replies. Each value_request is answered, in order, with a `value_reply` carrying ask's value, or
// null when ask is nil or declines; the value is never logged. Each step_progress goes to progress
// (dropped when it is nil), all of them before the reply returns.
func (w *Worker) RequestAsk(ctx context.Context, typ string, body Message, ask Ask, progress Progress) (Message, error) {
	s := w.current()
	if s == nil {
		return nil, ErrUnavailable
	}
	key := ""
	if modelRequests[typ] && w.key != nil {
		var err error
		if key, err = w.key(ctx); err != nil {
			return nil, err
		}
	}
	id := fmt.Sprintf("e%d", w.seq.Add(1))
	p, err := s.register(id)
	if err != nil {
		return nil, err
	}
	defer s.unregister(id)
	// A caller who gives up tells the worker: its run would otherwise go on with nobody listening, its next
	// ask never answered, holding its site's browser for good.
	givenUp := func() { _ = s.send(Message{"id": id, "type": "cancel"}) }

	// ask can block for minutes; a worker that dies meanwhile must cancel it, not wait it out.
	askCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-askCtx.Done():
		}
	}()

	msg := Message{}
	for k, v := range body {
		msg[k] = v
	}
	msg["id"], msg["type"] = id, typ
	if modelRequests[typ] {
		msg["openrouter_key"] = key
	}
	if err := s.send(msg); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	// drain hands progress every frame already waiting: the socket keeps their order, the channels
	// don't, so a reply or an ask never overtakes the progress sent before it.
	drain := func() {
		for {
			select {
			case m := <-p.progress:
				if progress != nil {
					event, _ := m["event"].(string)
					payload, _ := m["payload"].(map[string]any)
					progress(event, payload)
				}
			default:
				return
			}
		}
	}
	for {
		select {
		case m, ok := <-p.progress:
			if ok && progress != nil {
				event, _ := m["event"].(string)
				payload, _ := m["payload"].(map[string]any)
				progress(event, payload)
			}
		case m, ok := <-p.reply:
			drain()
			if !ok {
				return nil, ErrUnavailable
			}
			if m["type"] == "error" {
				code, _ := m["code"].(string)
				text, _ := m["message"].(string)
				return nil, &WorkerError{Code: code, Message: text}
			}
			return m, nil
		case m, ok := <-p.asks:
			drain()
			if !ok {
				return nil, ErrUnavailable
			}
			askID, _ := m["ask_id"].(string)
			q := AskRequest{}
			q.Name, _ = m["name"].(string)
			q.Why, _ = m["why"].(string)
			if options, ok := m["options"].([]any); ok {
				q.Options = []string{}
				for _, o := range options {
					if text, ok := o.(string); ok {
						q.Options = append(q.Options, text)
					}
				}
			}
			var value any // null unless ask supplies one
			if ask != nil {
				if v, ok := ask(askCtx, q); ok {
					value = v
				}
			}
			if err := ctx.Err(); err != nil {
				drain()
				givenUp()
				return nil, err
			}
			select {
			case <-s.done:
				return nil, ErrUnavailable
			default:
			}
			if err := s.send(Message{"id": id, "type": "value_reply", "ask_id": askID, "value": value}); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
			}
		case <-ctx.Done():
			drain()
			givenUp()
			return nil, ctx.Err()
		}
	}
}

// Status pings the worker when one is connected.
func (w *Worker) Status(ctx context.Context) Status {
	s := w.current()
	if s == nil {
		return Status{}
	}
	st := Status{Connected: true, WorkerVersion: s.version}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	pong, err := w.Request(ctx, "health", nil)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if ms, ok := pong["uptime_ms"].(float64); ok {
		st.UptimeMS = int64(ms)
	}
	return st
}

func (w *Worker) current() *session {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cur
}

// handle serves one connection until it drops. connected says the handshake passed; err is why it
// did not (nil once it passed).
func (w *Worker) handle(conn net.Conn) (connected bool, err error) {
	dec := json.NewDecoder(conn)
	var hello Message
	conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	if err := dec.Decode(&hello); err != nil {
		conn.Close()
		return false, fmt.Errorf("no hello: %w", err)
	}
	if hello["type"] != "hello" || !compatible(hello["contract_version"]) {
		conn.Close()
		return false, fmt.Errorf("refused: type %v, contract_version %v", hello["type"], hello["contract_version"])
	}
	conn.SetReadDeadline(time.Time{})

	version, _ := hello["worker_version"].(string)
	w.mu.Lock()
	s := &session{conn: conn, enc: json.NewEncoder(conn), version: version, pending: map[string]*pendingReq{}, done: make(chan struct{})}
	w.cur = s
	w.mu.Unlock()
	if err := s.send(Message{"id": hello["id"], "type": "ready"}); err != nil {
		slog.Warn("browser worker: ready not sent", "err", err)
	}
	slog.Info("browser worker connected", "worker_version", version)

	for {
		var m Message
		if err := dec.Decode(&m); err != nil {
			break
		}
		s.deliver(m)
	}
	w.mu.Lock()
	if w.cur == s {
		w.cur = nil
	}
	w.mu.Unlock()
	s.close()
	slog.Info("browser worker disconnected")
	return true, nil
}

func compatible(v any) bool {
	n, ok := v.(float64)
	if !ok {
		return false
	}
	d := int(n) - ContractVersion
	return d >= -1 && d <= 1
}

// session is one live worker connection.
type session struct {
	conn    net.Conn
	version string

	sendMu sync.Mutex
	enc    *json.Encoder

	mu      sync.Mutex
	closed  bool
	done    chan struct{} // closed with the session, to cancel an ask in flight
	pending map[string]*pendingReq
}

// pendingReq is what a request waits on: its final reply, the worker's value_requests, and its
// step_progress.
type pendingReq struct {
	reply    chan Message
	asks     chan Message
	progress chan Message
}

func (s *session) send(m Message) error {
	m["contract_version"] = ContractVersion
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.enc.Encode(m)
}

func (s *session) register(id string) (*pendingReq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrUnavailable
	}
	p := &pendingReq{reply: make(chan Message, 1), asks: make(chan Message, 1), progress: make(chan Message, progressBuffer)}
	s.pending[id] = p
	return p, nil
}

func (s *session) unregister(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, id)
}

// askBuffer is one: the worker sends a value_request and waits for its reply before the next.

// deliver routes a frame to its waiting request: value_request to its asks, step_progress to its
// progress (dropped when that is full), anything else with that id to its reply. A frame no request
// is waiting for goes nowhere.
func (s *session) deliver(m Message) {
	id, _ := m["id"].(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok {
		return
	}
	if m["type"] == "step_progress" {
		select {
		case p.progress <- m:
		default:
		}
		return
	}
	if m["type"] == "value_request" {
		select {
		case p.asks <- m:
		default:
			// A second ask while one is unanswered breaks the contract: fail the request.
			p.reply <- Message{"type": "error", "code": "ask_overflow", "message": "value_request while another is unanswered"}
			delete(s.pending, id)
		}
		return
	}
	p.reply <- m
	delete(s.pending, id)
}

func (s *session) close() {
	s.conn.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.done)
	for id, p := range s.pending {
		close(p.reply)
		close(p.asks)
		delete(s.pending, id)
	}
}
