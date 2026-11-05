// Package realtime is a client for OpenAI's Realtime API over a WebSocket: phone-quality audio (G.711
// μ-law, 8 kHz) both ways, a transcript of what the other party says, and the model speaking only
// when asked (or, in Lead mode, answering each turn itself). It holds no logic of its own about what a call is for: callers (calld) script the
// turns and judge every answer. Shapes verified live on 2026-10-28
// (docs/superpowers/specs/2026-10-28-voice-two-way-design.md).
package realtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ErrRefused is OpenAI refusing the key: invalid, revoked, out of quota, or without the model.
var ErrRefused = errors.New("OpenAI refused the key")

// Config: URL is the endpoint without its query (wss://api.openai.com/v1/realtime); Voice defaults
// to marin; Language (ISO 639-1, optional) tells the transcriber what the other party speaks.
//
// Lead hands the turns to the model: server VAD creates a reply to each thing the other party says
// (still never cutting itself off), and Tools are the functions it may call. Without Lead the caller
// scripts every reply with Respond.
type Config struct {
	URL, Key, Model, Voice, Transcribe, Language string
	Lead                                         bool
	Tools                                        []Tool
}

// Tool is a function the model may call; Parameters is its JSON Schema.
type Tool struct {
	Name, Description string
	Parameters        map[string]any
}

// ToolCall is a finished call of a tool; Arguments is the model's JSON text, unparsed.
type ToolCall struct{ Name, Arguments string }

// collision is what OpenAI says when a reply is asked for while another is running. Respond waits for
// a running reply it knows of; one the server starts between that check and the send still collides,
// harmlessly.
const collision = "conversation_already_has_active_response"

// A session must notice a peer that went silent — a route that died without a reset never closes
// the socket — within seconds, not when the kernel gives up: it pings every pingEvery and ends when
// a pong takes longer than pingWait; a stretch of audio that can't be sent within appendWait ends it
// too (by then the audio is useless anyway).
var (
	pingEvery  = 5 * time.Second
	pingWait   = 5 * time.Second
	appendWait = 2 * time.Second
)

// refusals are the error codes that mean the key will not do.
var refusals = map[string]bool{"invalid_api_key": true, "insufficient_quota": true, "model_not_found": true}

// Session is one open Realtime session. Its channels are fed by one reader — the audio through a
// queue as long as the reader is ahead of whoever plays it; Done closes when the socket ends, for any
// reason — an error event included — and Err says why.
type Session struct {
	c      *websocket.Conn
	cancel context.CancelFunc
	wmu    sync.Mutex

	qmu    sync.Mutex
	queue  [][]byte      // the model's voice, read but not yet handed to audio: a reply comes faster than it plays
	queued chan struct{} // the reader added to queue
	audio  chan []byte
	heard  chan string
	said   chan string
	tools  chan ToolCall
	spoken chan struct{}
	done   chan struct{}

	once sync.Once
	err  error

	rmu     sync.Mutex
	active  bool    // a reply is running: from response.created to its response.done
	pending *string // the instructions of the reply asked for meanwhile, sent once it is done

	pingEvery, pingWait, appendWait time.Duration // the bounds as they were when it opened

	usageMu sync.Mutex
	usage   Usage
}

// Tokens is what one model used over a session, as OpenAI reports it. Inputs are split into what
// was billed in full and what was served from the cache (the reply's own counts include the cached
// ones; these do not), because the two are priced an order of magnitude apart.
type Tokens struct {
	Model         string `json:"model"`
	TextIn        int    `json:"text_in"`
	CachedTextIn  int    `json:"cached_text_in,omitempty"`
	AudioIn       int    `json:"audio_in"`
	CachedAudioIn int    `json:"cached_audio_in,omitempty"`
	TextOut       int    `json:"text_out"`
	AudioOut      int    `json:"audio_out,omitempty"`
}

// Usage is a session's two bills: the voice model's replies, and the transcriber's transcripts of
// the other party. OpenAI sends counts, never prices — the cost is the caller's arithmetic.
type Usage struct {
	Call          Tokens `json:"call"`
	Transcription Tokens `json:"transcription"`
}

// usage is one event's token report: a reply's (response.usage) or a transcript's (usage).
type usage struct {
	Type         string `json:"type"` // "tokens" for a token-billed transcriber; "duration" carries none
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	InputDetails struct {
		TextTokens    int `json:"text_tokens"`
		AudioTokens   int `json:"audio_tokens"`
		CachedDetails struct {
			TextTokens  int `json:"text_tokens"`
			AudioTokens int `json:"audio_tokens"`
		} `json:"cached_tokens_details"`
	} `json:"input_token_details"`
	OutputDetails struct {
		TextTokens  int `json:"text_tokens"`
		AudioTokens int `json:"audio_tokens"`
	} `json:"output_token_details"`
}

type event struct {
	Type       string `json:"type"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments"`
	Usage      *usage `json:"usage"`
	Response   *struct {
		Status string `json:"status"`
		Usage  *usage `json:"usage"`
	} `json:"response"`
	Error *struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"error"`
}

// Open dials, sends the session (μ-law in and out, input transcription, server turn detection that
// never answers on its own) and waits until OpenAI has applied it.
func Open(ctx context.Context, cfg Config, instructions string) (*Session, error) {
	voice := cfg.Voice
	if voice == "" {
		voice = "marin"
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+cfg.Key)
	c, resp, err := websocket.Dial(ctx, cfg.URL+"?model="+cfg.Model, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
			resp.StatusCode == http.StatusNotFound) { // 404: the model is gone, which the key can't use either
			return nil, ErrRefused
		}
		return nil, fmt.Errorf("realtime: dial: %T", err) // the type only: the error text may echo the request
	}
	c.SetReadLimit(1 << 22)
	format := map[string]string{"type": "audio/pcmu"} // with "rate", OpenAI answers unknown_parameter
	transcription := map[string]string{"model": cfg.Transcribe}
	if cfg.Language != "" {
		transcription["language"] = cfg.Language
	}
	s := &Session{c: c, queued: make(chan struct{}, 1), audio: make(chan []byte, 64), heard: make(chan string, 8), said: make(chan string, 16), tools: make(chan ToolCall, 4), spoken: make(chan struct{}, 8), done: make(chan struct{}),
		pingEvery: pingEvery, pingWait: pingWait, appendWait: appendWait,
		usage: Usage{Call: Tokens{Model: cfg.Model}, Transcription: Tokens{Model: cfg.Transcribe}}}
	session := map[string]any{
		"type":              "realtime",
		"instructions":      instructions,
		"output_modalities": []string{"audio"},
		"audio": map[string]any{
			"input": map[string]any{
				"format":         format,
				"transcription":  transcription,
				"turn_detection": map[string]any{"type": "server_vad", "create_response": cfg.Lead, "interrupt_response": false},
			},
			"output": map[string]any{"format": format, "voice": voice},
		},
	}
	if len(cfg.Tools) > 0 {
		tools := make([]map[string]any, len(cfg.Tools))
		for i, t := range cfg.Tools {
			tools[i] = map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters}
		}
		session["tools"], session["tool_choice"] = tools, "auto"
	}
	if err := s.send(ctx, map[string]any{"type": "session.update", "session": session}); err != nil {
		c.CloseNow()
		return nil, err
	}
	for {
		ev, err := s.read(ctx)
		if err != nil {
			c.CloseNow()
			return nil, err
		}
		if ev.Type == "session.updated" {
			break
		}
		if ev.Type == "error" {
			c.CloseNow()
			return nil, ev.err()
		}
	}
	rctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.run(rctx)
	go s.forward(rctx)
	go s.ping(rctx)
	return s, nil
}

func (e event) err() error {
	if e.Error == nil {
		return errors.New("realtime: error event")
	}
	if refusals[e.Error.Code] {
		return ErrRefused
	}
	return fmt.Errorf("realtime: %s (%s)", e.Error.Code, e.Error.Type)
}

func (s *Session) read(ctx context.Context) (event, error) {
	_, b, err := s.c.Read(ctx)
	if err != nil {
		return event{}, fmt.Errorf("realtime: read: %T", err)
	}
	var ev event
	if err := json.Unmarshal(b, &ev); err != nil {
		return event{}, fmt.Errorf("realtime: event: %w", err)
	}
	return ev, nil
}

func (s *Session) run(ctx context.Context) {
	for {
		ev, err := s.read(ctx)
		if err != nil {
			s.end(err)
			return
		}
		switch ev.Type {
		case "response.output_audio.delta":
			b, err := base64.StdEncoding.DecodeString(ev.Delta)
			if err != nil {
				continue
			}
			s.qmu.Lock() // queued, never dropped and never holding the socket
			s.queue = append(s.queue, b)
			s.qmu.Unlock()
			select {
			case s.queued <- struct{}{}:
			default: // the forwarder is already woken
			}
		case "conversation.item.input_audio_transcription.completed":
			s.countTranscript(ev.Usage) // before the transcript is handed on, so a reader sees it counted
			select {
			case s.heard <- ev.Transcript:
			case <-ctx.Done(): // closed while nobody reads
				return
			}
		case "response.output_audio_transcript.done":
			select {
			case s.said <- ev.Transcript:
			default: // unlike audio, a new transcript is dropped: nobody reading it is debug material, not worth holding the socket
			}
		case "response.created":
			s.rmu.Lock()
			s.active = true
			s.rmu.Unlock()
		case "response.done":
			if ev.Response != nil {
				s.countReply(ev.Response.Usage) // billed even when it didn't complete
			}
			s.rmu.Lock()
			next := s.pending
			s.active, s.pending = false, nil
			s.rmu.Unlock()
			if ev.Response == nil || ev.Response.Status != "completed" { // failed, cancelled, incomplete: nothing more will be said
				status := "unknown"
				if ev.Response != nil {
					status = ev.Response.Status
				}
				s.end(fmt.Errorf("realtime: response %s", status))
				return
			}
			if next != nil {
				wctx, cancel := context.WithTimeout(ctx, s.appendWait)
				err := s.send(wctx, create(*next))
				cancel()
				if err != nil {
					s.end(err)
					return
				}
			}
			select {
			case s.spoken <- struct{}{}:
			case <-ctx.Done():
				return
			}
		case "response.function_call_arguments.done":
			select {
			case s.tools <- ToolCall{Name: ev.Name, Arguments: ev.Arguments}:
			case <-ctx.Done():
				return
			}
		case "error":
			if ev.Error != nil && ev.Error.Code == collision {
				continue
			}
			s.end(ev.err())
			return
		}
	}
}

// forward hands the queued voice to audio in order, as fast as it is taken, until the session ends.
func (s *Session) forward(ctx context.Context) {
	for {
		s.qmu.Lock()
		if len(s.queue) == 0 {
			s.qmu.Unlock()
			select {
			case <-s.queued:
				continue
			case <-ctx.Done():
				return
			}
		}
		b := s.queue[0]
		s.queue[0], s.queue = nil, s.queue[1:]
		s.qmu.Unlock()
		select {
		case s.audio <- b:
		case <-ctx.Done():
			return
		}
	}
}

// ping ends the session when the peer stops answering.
func (s *Session) ping(ctx context.Context) {
	t := time.NewTicker(s.pingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, s.pingWait)
		err := s.c.Ping(pctx)
		cancel()
		if err != nil && ctx.Err() == nil {
			s.end(fmt.Errorf("realtime: no pong: %T", err))
			return
		}
	}
}

func (s *Session) end(err error) {
	s.once.Do(func() {
		s.err = err
		if s.cancel != nil {
			s.cancel()
		}
		s.c.CloseNow() // no closing handshake: a stalled peer would only delay it
		close(s.done)
	})
}

func (s *Session) send(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if err := s.c.Write(ctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("realtime: write: %T", err)
	}
	return nil
}

// Append sends a stretch of the other party's audio (μ-law). One that can't be sent within
// appendWait ends the session.
func (s *Session) Append(ulaw []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.appendWait)
	defer cancel()
	err := s.send(ctx, map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(ulaw)})
	if err != nil {
		s.end(err)
	}
	return err
}

// Respond has the model speak, following instructions; empty ones leave the session's in force.
// While a reply is running it is asked for as soon as that one is done — the latest asked for, if
// more than one.
func (s *Session) Respond(ctx context.Context, instructions string) error {
	s.rmu.Lock()
	if s.active {
		s.pending = &instructions
		s.rmu.Unlock()
		return nil
	}
	s.rmu.Unlock()
	return s.send(ctx, create(instructions))
}

func create(instructions string) map[string]any {
	response := map[string]string{}
	if instructions != "" {
		response["instructions"] = instructions
	}
	return map[string]any{"type": "response.create", "response": response}
}

func (s *Session) Audio() <-chan []byte { return s.audio }
func (s *Session) Heard() <-chan string { return s.heard }

// Said yields the transcript of each response the model spoke, as the model said it.
func (s *Session) Said() <-chan string     { return s.said }
func (s *Session) Spoken() <-chan struct{} { return s.spoken }

// ToolCalls yields each function the model called, once its arguments are complete.
func (s *Session) ToolCalls() <-chan ToolCall { return s.tools }
func (s *Session) Done() <-chan struct{}      { return s.done }

// Err is why the session ended (nil while it runs, or after Close).
func (s *Session) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}

func (s *Session) Close() error {
	s.end(nil)
	return nil
}

// Usage is what the session has used so far.
func (s *Session) Usage() Usage {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	return s.usage
}

func (s *Session) countReply(u *usage) {
	if u == nil {
		return
	}
	cachedText, cachedAudio := u.InputDetails.CachedDetails.TextTokens, u.InputDetails.CachedDetails.AudioTokens
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	c := &s.usage.Call
	c.TextIn += u.InputDetails.TextTokens - cachedText
	c.CachedTextIn += cachedText
	c.AudioIn += u.InputDetails.AudioTokens - cachedAudio
	c.CachedAudioIn += cachedAudio
	c.TextOut += u.OutputDetails.TextTokens
	c.AudioOut += u.OutputDetails.AudioTokens
}

func (s *Session) countTranscript(u *usage) {
	if u == nil || u.Type != "tokens" {
		return
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	t := &s.usage.Transcription
	if u.InputDetails.TextTokens+u.InputDetails.AudioTokens == 0 {
		t.AudioIn += u.InputTokens // only the total given: a transcriber's input is the audio
	} else {
		t.TextIn += u.InputDetails.TextTokens
		t.AudioIn += u.InputDetails.AudioTokens
	}
	t.TextOut += u.OutputTokens
}
