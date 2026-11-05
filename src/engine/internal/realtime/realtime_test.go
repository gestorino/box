package realtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var ctx = context.Background()

// fake is an OpenAI Realtime server on loopback: it records each client event and runs script on
// the connection once the client's session.update arrived.
type fake struct {
	mu     sync.Mutex
	got    []map[string]any
	auth   string
	status int  // answer the upgrade with this instead, when set
	stall  bool // after the session is set up, stop reading: a peer gone silent on a dead route
	script func(c *websocket.Conn)
}

func (f *fake) events() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.got...)
}

func (f *fake) serve(t *testing.T) Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = r.Header.Get("Authorization")
		f.mu.Unlock()
		if f.status != 0 {
			http.Error(w, `{"error":{"code":"invalid_api_key"}}`, f.status)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		send(c, `{"type":"session.created"}`)
		first := true
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var ev map[string]any
			json.Unmarshal(b, &ev)
			f.mu.Lock()
			f.got = append(f.got, ev)
			f.mu.Unlock()
			if first && ev["type"] == "session.update" && f.stall {
				send(c, `{"type":"session.updated","session":{}}`)
				<-r.Context().Done()
				return
			}
			if first && ev["type"] == "session.update" {
				first = false
				if f.script != nil {
					go f.script(c)
				} else {
					send(c, `{"type":"session.updated","session":{}}`)
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Key: "sk-secret", Model: "gpt-realtime-2.1", Transcribe: "gpt-4o-transcribe"}
}

func send(c *websocket.Conn, ev string) {
	c.Write(context.Background(), websocket.MessageText, []byte(ev))
}

func open(t *testing.T, f *fake) *Session { return openWith(t, f, nil) }

func openWith(t *testing.T, f *fake, tweak func(*Config)) *Session {
	t.Helper()
	cfg := f.serve(t)
	if tweak != nil {
		tweak(&cfg)
	}
	s, err := Open(ctx, cfg, "Ask the sum.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOpenSendsTheSession(t *testing.T) {
	f := &fake{}
	open(t, f)
	if f.auth != "Bearer sk-secret" {
		t.Fatalf("auth %q", f.auth)
	}
	ev := f.events()[0]
	b, _ := json.Marshal(ev)
	for _, want := range []string{
		`"type":"session.update"`, `"type":"realtime"`, `"instructions":"Ask the sum."`,
		`"input":{"format":{"type":"audio/pcmu"}`, `"output":{"format":{"type":"audio/pcmu"}`,
		`"transcription":{"model":"gpt-4o-transcribe"}`, `"create_response":false`, `"interrupt_response":false`,
		`"type":"server_vad"`, `"voice":"marin"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("no %s in %s", want, b)
		}
	}
	if strings.Contains(string(b), "rate") {
		t.Errorf("OpenAI refuses a rate on audio/pcmu: %s", b)
	}
}

// The call's language helps the transcriber read number words.
func TestOpenSendsTheTranscriptionLanguage(t *testing.T) {
	f := &fake{}
	cfg := f.serve(t)
	cfg.Language = "pt"
	s, err := Open(ctx, cfg, "x")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b, _ := json.Marshal(f.events()[0])
	if !strings.Contains(string(b), `"transcription":{"language":"pt","model":"gpt-4o-transcribe"}`) {
		t.Fatalf("%s", b)
	}
}

func TestOpenRefused(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} { // 404: the model is gone
		if _, err := Open(ctx, (&fake{status: status}).serve(t), "x"); !errors.Is(err, ErrRefused) {
			t.Fatalf("%d: %v", status, err)
		}
	}
	for _, code := range []string{"invalid_api_key", "insufficient_quota", "model_not_found"} {
		f := &fake{script: func(c *websocket.Conn) {
			send(c, `{"type":"error","error":{"type":"invalid_request_error","code":"`+code+`","message":"no"}}`)
		}}
		if _, err := Open(ctx, f.serve(t), "x"); !errors.Is(err, ErrRefused) {
			t.Fatalf("%s: %v", code, err)
		}
	}
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"error","error":{"type":"invalid_request_error","code":"unknown_parameter","message":"no"}}`)
	}}
	if _, err := Open(ctx, f.serve(t), "x"); err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "unknown_parameter") {
		t.Fatalf("another error: %v", err)
	}
}

func TestAppendAndRespond(t *testing.T) {
	f := &fake{}
	s := open(t, f)
	if err := s.Append([]byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.Respond(ctx, "say x"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both events", func() bool { return len(f.events()) == 3 })
	ev := f.events()
	if ev[1]["type"] != "input_audio_buffer.append" || ev[1]["audio"] != "AQI=" {
		t.Fatalf("%v", ev[1])
	}
	r, _ := ev[2]["response"].(map[string]any)
	if ev[2]["type"] != "response.create" || r["instructions"] != "say x" {
		t.Fatalf("%v", ev[2])
	}
}

func TestEventsReachTheirChannels(t *testing.T) {
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		send(c, `{"type":"response.output_audio.delta","delta":"AQI="}`)
		send(c, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"forty two"}`)
		send(c, `{"type":"some.future.event"}`)
		send(c, `{"type":"response.done","response":{"status":"completed"}}`)
	}}
	s := open(t, f)
	select {
	case a := <-s.Audio():
		if string(a) != "\x01\x02" {
			t.Fatalf("%v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no audio")
	}
	select {
	case h := <-s.Heard():
		if h != "forty two" {
			t.Fatal(h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing heard")
	}
	select {
	case <-s.Spoken():
	case <-time.After(2 * time.Second):
		t.Fatal("no response.done")
	}
}

// A reply comes faster than it plays: however far behind the reader is, no audio is dropped.
func TestAudioIsNeverDroppedWhileTheReaderIsBehind(t *testing.T) {
	const n = 500
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		for i := range n {
			send(c, fmt.Sprintf(`{"type":"response.output_audio.delta","delta":%q}`, base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(i)))))
		}
		send(c, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"after the reply"}`)
	}}
	s := open(t, f)
	select {
	case <-s.Heard(): // every delta was read before it
	case <-time.After(2 * time.Second):
		t.Fatal("nothing heard")
	}
	for i := range n {
		select {
		case a := <-s.Audio():
			if string(a) != strconv.Itoa(i) {
				t.Fatalf("chunk %d is %q", i, a)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("chunk %d never came", i)
		}
	}
}

func TestDoneWhenTheServerCloses(t *testing.T) {
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		time.Sleep(20 * time.Millisecond)
		c.Close(websocket.StatusGoingAway, "bye")
	}}
	s := open(t, f)
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("not done")
	}
	if s.Err() == nil {
		t.Fatal("no reason")
	}
	s.Close()
	s.Close() // idempotent
}

func TestAnErrorMidSessionEndsIt(t *testing.T) {
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		send(c, `{"type":"error","error":{"type":"server_error","code":"server_error","message":"x"}}`)
	}}
	s := open(t, f)
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("an error event left the session running")
	}
	if !strings.Contains(s.Err().Error(), "server_error") {
		t.Fatal(s.Err())
	}
}

func TestNoSecretInErrors(t *testing.T) {
	cfg := (&fake{status: http.StatusUnauthorized}).serve(t)
	_, err := Open(ctx, cfg, "x")
	cfg.URL = "ws://127.0.0.1:1"
	_, err2 := Open(ctx, cfg, "x")
	for _, e := range []error{err, err2} {
		if e == nil || strings.Contains(e.Error(), "sk-secret") {
			t.Fatalf("%v", e)
		}
	}
}

// shorten sets the stall bounds for one test.
func shorten(t *testing.T, ping, wait, appendLimit time.Duration) {
	t.Helper()
	p, w, a := pingEvery, pingWait, appendWait
	pingEvery, pingWait, appendWait = ping, wait, appendLimit
	t.Cleanup(func() { pingEvery, pingWait, appendWait = p, w, a })
}

// A peer that stops answering (the route died without a reset) ends the session within seconds,
// whatever the call is doing, so the call hangs up at once.
func TestAStalledPeerEndsTheSession(t *testing.T) {
	shorten(t, 50*time.Millisecond, 100*time.Millisecond, time.Second)
	s := open(t, &fake{stall: true})
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a silent peer left the session running")
	}
}

// Audio that can't be sent is given up on, never queued forever: the send fails and the session ends.
func TestAppendGivesUpOnAStalledPeer(t *testing.T) {
	shorten(t, time.Hour, time.Hour, 100*time.Millisecond)
	s := open(t, &fake{stall: true})
	chunk := make([]byte, 4096)
	var err error
	for deadline := time.Now().Add(10 * time.Second); err == nil && time.Now().Before(deadline); {
		err = s.Append(chunk)
	}
	if err == nil {
		t.Fatal("Append never gave up")
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("the session outlived a failed send")
	}
}

// A response OpenAI could not finish (quota, a server failure) ends the session: no dead air.
func TestAFailedResponseEndsTheSession(t *testing.T) {
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		send(c, `{"type":"response.done","response":{"status":"failed","status_details":{"type":"failed","error":{"code":"insufficient_quota"}}}}`)
	}}
	s := open(t, f)
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a failed response left the session running")
	}
	if !strings.Contains(s.Err().Error(), "failed") {
		t.Fatal(s.Err())
	}
	select {
	case <-s.Spoken():
		t.Fatal("a failed response counted as spoken")
	default:
	}
}

func TestSaidCarriesTheSpokenTranscript(t *testing.T) {
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		send(c, `{"type":"response.output_audio_transcript.done","transcript":"Верно, спасибо. До свидания."}`)
	}}
	s := open(t, f)
	select {
	case said := <-s.Said():
		if said != "Верно, спасибо. До свидания." {
			t.Fatal(said)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing said")
	}
}

func TestAFullSaidBufferNeverStallsTheSession(t *testing.T) {
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		for i := 0; i < 20; i++ { // more than the buffer holds, and nobody reads Said
			send(c, `{"type":"response.output_audio_transcript.done","transcript":"x"}`)
		}
		send(c, `{"type":"response.output_audio.delta","delta":"AQI="}`)
	}}
	s := open(t, f)
	select {
	case <-s.Audio():
	case <-s.Done():
		t.Fatalf("the session ended: %v", s.Err())
	case <-time.After(2 * time.Second):
		t.Fatal("the reader is stuck behind an unread transcript")
	}
}

// The session adds up what each reply and each transcript cost in tokens, and names its two models.
// The realtime reply's token counts include the cached ones; Usage keeps them apart (uncached + cached).
func TestUsageSumsRepliesAndTranscripts(t *testing.T) {
	reply := `{"type":"response.done","response":{"status":"completed","usage":{"input_tokens":1500,"output_tokens":900,` +
		`"input_token_details":{"text_tokens":300,"audio_tokens":1200,"cached_tokens":250,"cached_tokens_details":{"text_tokens":200,"audio_tokens":50}},` +
		`"output_token_details":{"text_tokens":100,"audio_tokens":800}}}}`
	heard := `{"type":"conversation.item.input_audio_transcription.completed","transcript":"сорок два",` +
		`"usage":{"type":"tokens","input_tokens":120,"output_tokens":8,"input_token_details":{"text_tokens":20,"audio_tokens":100}}}`
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		send(c, reply)
		send(c, heard)
		send(c, reply)
	}}
	s := open(t, f)
	go func() {
		for range s.Heard() {
		}
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-s.Spoken():
		case <-time.After(2 * time.Second):
			t.Fatal("no reply")
		}
	}
	eventually(t, "the transcript's usage", func() bool { return s.Usage().Transcription.AudioIn == 100 })
	u := s.Usage()
	call := Tokens{Model: "gpt-realtime-2.1", TextIn: 200, CachedTextIn: 400, AudioIn: 2300, CachedAudioIn: 100, TextOut: 200, AudioOut: 1600}
	if u.Call != call {
		t.Fatalf("call = %+v, want %+v", u.Call, call)
	}
	tr := Tokens{Model: "gpt-4o-transcribe", TextIn: 20, AudioIn: 100, TextOut: 8}
	if u.Transcription != tr {
		t.Fatalf("transcription = %+v, want %+v", u.Transcription, tr)
	}
}

// A reply that didn't complete (the other side hung up mid-word) was still billed: it is counted
// before the session ends.
func TestAnUnfinishedReplyIsStillCounted(t *testing.T) {
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		send(c, `{"type":"response.done","response":{"status":"incomplete","usage":{"input_tokens":500,"output_tokens":300,`+
			`"input_token_details":{"text_tokens":100,"audio_tokens":400},"output_token_details":{"text_tokens":50,"audio_tokens":250}}}}`)
	}}
	s := open(t, f)
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the session did not end")
	}
	if c := s.Usage().Call; c.AudioIn != 400 || c.TextIn != 100 || c.AudioOut != 250 || c.TextOut != 50 {
		t.Fatalf("call = %+v", c)
	}
}

// A transcript whose usage gives only totals counts its input as audio (what a transcriber takes),
// never as nothing.
func TestATranscriptWithOnlyTotalsCountsItsInputAsAudio(t *testing.T) {
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		send(c, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"ok","usage":{"type":"tokens","input_tokens":90,"output_tokens":3}}`)
	}}
	s := open(t, f)
	go func() {
		for range s.Heard() {
		}
	}()
	eventually(t, "the transcript's usage", func() bool { return s.Usage().Transcription.TextOut == 3 })
	if tr := s.Usage().Transcription; tr.AudioIn != 90 || tr.TextIn != 0 {
		t.Fatalf("transcription = %+v", tr)
	}
}

func TestLeadLetsTheModelAnswerEachTurn(t *testing.T) {
	f := &fake{}
	openWith(t, f, func(c *Config) {
		c.Lead = true
		c.Tools = []Tool{{Name: "end_call", Description: "hang up", Parameters: map[string]any{"type": "object"}}}
	})
	sess := f.events()[0]["session"].(map[string]any)
	td := sess["audio"].(map[string]any)["input"].(map[string]any)["turn_detection"].(map[string]any)
	if td["create_response"] != true || td["interrupt_response"] != false {
		t.Fatalf("turn_detection %v", td)
	}
	tool := sess["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "end_call" || tool["description"] != "hang up" || sess["tool_choice"] != "auto" {
		t.Fatalf("tools %v, tool_choice %v", sess["tools"], sess["tool_choice"])
	}
}

func TestScriptedSessionsLeaveRepliesToTheCaller(t *testing.T) {
	f := &fake{}
	open(t, f)
	sess := f.events()[0]["session"].(map[string]any)
	td := sess["audio"].(map[string]any)["input"].(map[string]any)["turn_detection"].(map[string]any)
	if td["create_response"] != false {
		t.Fatalf("turn_detection %v", td)
	}
	if _, ok := sess["tools"]; ok {
		t.Fatalf("tools sent: %v", sess["tools"])
	}
	if _, ok := sess["tool_choice"]; ok {
		t.Fatalf("tool_choice sent: %v", sess["tool_choice"])
	}
}

func TestAToolCallReachesToolCalls(t *testing.T) {
	args := `{"result":"done","summary":"Booked."}`
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		send(c, `{"type":"response.function_call_arguments.done","name":"end_call","call_id":"c1","arguments":"{\"result\":\"done\",\"summary\":\"Booked.\"}"}`)
	}}
	s := open(t, f)
	select {
	case got := <-s.ToolCalls():
		if got != (ToolCall{Name: "end_call", Arguments: args}) {
			t.Fatalf("%+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no tool call")
	}
}

// Server VAD can start a reply while the caller's own response.create is in flight.
func TestARespondCollisionDoesNotEndTheSession(t *testing.T) {
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		send(c, `{"type":"error","error":{"code":"conversation_already_has_active_response","message":"x"}}`)
		send(c, `{"type":"response.done","response":{"status":"completed"}}`)
	}}
	s := open(t, f)
	select {
	case <-s.Spoken():
	case <-time.After(2 * time.Second):
		t.Fatal("no spoken")
	}
	select {
	case <-s.Done():
		t.Fatalf("collision ended the session: %v", s.Err())
	default:
	}
}

func TestRespondWithoutInstructionsSendsNone(t *testing.T) {
	f := &fake{}
	s := open(t, f)
	if err := s.Respond(ctx, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "response.create", func() bool { return len(f.events()) == 2 })
	ev := f.events()[1]
	r, _ := ev["response"].(map[string]any)
	if ev["type"] != "response.create" {
		t.Fatalf("%v", ev)
	}
	if _, ok := r["instructions"]; ok {
		t.Fatalf("instructions sent: %v", r)
	}
}

// creates are the response.create events the fake has received, by their instructions ("" for none).
func creates(f *fake) []string {
	var got []string
	for _, ev := range f.events() {
		if ev["type"] == "response.create" {
			r, _ := ev["response"].(map[string]any)
			instr, _ := r["instructions"].(string)
			got = append(got, instr)
		}
	}
	return got
}

// A reply asked for while another is running would be refused (and ignored): it waits for that one.
func TestARespondWaitsForTheActiveReply(t *testing.T) {
	release := make(chan struct{})
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		send(c, `{"type":"response.created","response":{"status":"in_progress"}}`)
		send(c, `{"type":"response.output_audio.delta","delta":"AQI="}`)
		<-release
		send(c, `{"type":"response.done","response":{"status":"completed"}}`)
	}}
	s := open(t, f)
	select {
	case <-s.Audio(): // read after response.created: the session knows a reply is running
	case <-time.After(2 * time.Second):
		t.Fatal("no audio")
	}
	if err := s.Respond(ctx, "say hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Respond(ctx, "say goodbye"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := creates(f); len(got) != 0 {
		t.Fatalf("sent over the active reply: %q", got)
	}
	close(release)
	eventually(t, "the waiting response.create", func() bool { return len(creates(f)) > 0 })
	time.Sleep(50 * time.Millisecond)
	if got := creates(f); len(got) != 1 || got[0] != "say goodbye" {
		t.Fatalf("response.create %q, want the latest one once", got)
	}
}

func TestARespondWithNoActiveReplyIsSentAtOnce(t *testing.T) {
	f := &fake{script: func(c *websocket.Conn) {
		send(c, `{"type":"session.updated","session":{}}`)
		send(c, `{"type":"response.created","response":{"status":"in_progress"}}`)
		send(c, `{"type":"response.done","response":{"status":"completed"}}`)
	}}
	s := open(t, f)
	select {
	case <-s.Spoken(): // the reply is over
	case <-time.After(2 * time.Second):
		t.Fatal("no spoken")
	}
	if err := s.Respond(ctx, "say goodbye"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "response.create", func() bool { return len(creates(f)) == 1 })
	if got := creates(f); got[0] != "say goodbye" {
		t.Fatalf("%q", got)
	}
}
