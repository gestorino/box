package converse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	agentv1 "concierge/engine/internal/gen/agent/v1"
	"concierge/engine/internal/secrets"
	"concierge/engine/internal/signins"
)

// fakeErrands stands in for signins.Service, safe for calls from many goroutines.
type fakeErrands struct {
	mu         sync.Mutex
	cards      []signins.Card
	registered map[string]bool
	started    []signins.Errand
	nextRun    int64
	startErr   error
	answers    map[int64][]string
	// beforeAnswer, when set, runs inside AnswerWait before it looks at the wait: a wait changing then.
	beforeAnswer func()
	stopped      []int64
	waiting      map[int64]string
	questions    map[int64]*signins.Question
	runs         map[int64]signins.AgentRunDetail
	appts        []signins.Appointment
	checked      []int64
}

func newFake() *fakeErrands {
	return &fakeErrands{
		cards: []signins.Card{
			{Key: "example.com", Name: "Example", Link: "https://www.example.com/start", Actions: []signins.Action{
				{Text: "Book a table", Params: []string{"When", "Who", "Note"}, Effect: "create"},
				{Text: "Cancel a visit", Params: []string{}, Effect: "cancel"},
			}},
			{Key: "example.org", Name: "Other", Link: "https://example.org/", Actions: []signins.Action{
				{Text: "Look around", Params: []string{}, Effect: "none"},
			}},
		},
		registered: map[string]bool{"example.com": true},
		nextRun:    40,
		answers:    map[int64][]string{},
		waiting:    map[int64]string{},
		questions:  map[int64]*signins.Question{},
		runs:       map[int64]signins.AgentRunDetail{},
	}
}

// wait makes the run wait on kind, its question's wait the given one (none for a human check).
func (f *fakeErrands) wait(run int64, kind string, wait int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waiting[run] = kind
	f.questions[run] = nil
	if wait != 0 {
		f.questions[run] = &signins.Question{Text: kind + "?", Options: []string{}, Wait: wait}
	}
}

func (f *fakeErrands) Cards(context.Context) ([]signins.Card, error) { return f.cards, nil }
func (f *fakeErrands) Registered(_ context.Context, key string) (bool, error) {
	return f.registered[key], nil
}
func (f *fakeErrands) StartErrand(_ context.Context, e signins.Errand) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return 0, f.startErr
	}
	f.started = append(f.started, e)
	f.nextRun++
	return f.nextRun, nil
}

// AnswerWait answers as the service does: only the run's current wait. It leaves the run waiting, as
// a Waiting read just before the answer still would.
func (f *fakeErrands) AnswerWait(run, wait int64, answer string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeAnswer != nil {
		f.beforeAnswer()
	}
	q := f.questions[run]
	if f.waiting[run] != "question" && f.waiting[run] != "confirm" {
		return signins.ErrNotWaiting
	}
	if q == nil || q.Wait != wait {
		return signins.ErrWaitChanged
	}
	f.answers[run] = append(f.answers[run], answer)
	return nil
}
func (f *fakeErrands) answered(run int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.answers[run]...)
}
func (f *fakeErrands) Stop(run int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, run)
	return nil
}
func (f *fakeErrands) AgentRun(_ context.Context, run int64) (signins.AgentRunDetail, bool, error) {
	d, ok := f.runs[run]
	return d, ok, nil
}
func (f *fakeErrands) Waiting(run int64) (string, *signins.Question) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.waiting[run], f.questions[run]
}
func (f *fakeErrands) Appointments(context.Context, string) ([]signins.Appointment, error) {
	return f.appts, nil
}
func (f *fakeErrands) CheckAppointment(_ context.Context, id int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checked = append(f.checked, id)
	f.nextRun++
	return f.nextRun, nil
}

func call(t *testing.T, tools *Tools, conv int64, name, args string) map[string]any {
	t.Helper()
	out := tools.Call(context.Background(), conv, 0, &agentv1.ToolCall{CallId: "c", Name: name, ArgumentsJson: args})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%s: not JSON: %s", name, out)
	}
	return m
}

func refused(t *testing.T, m map[string]any) string {
	t.Helper()
	msg, ok := m["error"].(string)
	if !ok || msg == "" {
		t.Fatalf("not refused: %v", m)
	}
	return msg
}

// refusedSaying is a refusal for the reason its words name.
func refusedSaying(t *testing.T, words string, m map[string]any) {
	t.Helper()
	if msg := refused(t, m); !strings.Contains(msg, words) {
		t.Fatalf("refused with %q, not for %q", msg, words)
	}
}

func TestListAccountsIsOnlyTheBoxsAccounts(t *testing.T) {
	f := newFake()
	tools := NewTools(f, openStore(t))
	out := tools.Call(context.Background(), 1, 0, &agentv1.ToolCall{Name: "list_accounts", ArgumentsJson: "{}"})
	want := `{"accounts":[{"site":"example.com","name":"Example","link":"https://www.example.com/start","actions":[` +
		`{"text":"Book a table","details":["When","Who","Note"]},{"text":"Cancel a visit","details":[]}]}]}`
	if out != want {
		t.Fatalf("list_accounts:\n%s\nwant\n%s", out, want)
	}
}

func TestStartErrandTakesOnlyACardsAction(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	for _, args := range []string{
		`{"site":"example.net","action":"Book a table","details":[],"appointment":0}`,
		`{"site":"example.org","action":"Look around","details":[],"appointment":0}`, // not the box's account
		`{"site":"example.com","action":"Book a room","details":[],"appointment":0}`,
		`{"site":"example.com","action":"Book a table","details":[{"name":"Where","value":"x"}],"appointment":0}`,
		`{"site":"example.com","action":"Book a table","details":[{"name":"Who","value":"2"},{"name":"Who","value":"3"}],"appointment":0}`,
	} {
		refused(t, call(t, tools, conv, "start_errand", args))
	}
	if len(f.started) != 0 {
		t.Fatalf("started %v", f.started)
	}

	m := call(t, tools, conv, "start_errand",
		`{"site":"example.com","action":"Book a table","details":[{"name":"Who","value":" 4 "},{"name":"When","value":"Fri 20:00"},{"name":"Note","value":"  "}],"appointment":0}`)
	if m["run"] != float64(41) {
		t.Fatalf("started: %v", m)
	}
	want := signins.Errand{URL: "https://www.example.com/start", Action: "Book a table (When: Fri 20:00; Who: 4)", Effect: "create"}
	if len(f.started) != 1 || f.started[0] != want {
		t.Fatalf("errand %+v, want %+v", f.started, want)
	}
	if got, ok, err := s.ConversationOfRun(context.Background(), 41); err != nil || !ok || got != conv {
		t.Fatalf("run 41 linked to %d %v %v", got, ok, err)
	}

	call(t, tools, conv, "start_errand", `{"site":"example.com","action":"Cancel a visit","details":[],"appointment":12}`)
	if want := (signins.Errand{URL: "https://www.example.com/start", Action: "Cancel a visit", Effect: "cancel", Appointment: 12}); f.started[1] != want {
		t.Fatalf("a cancel: %+v", f.started[1])
	}

	f.startErr = signins.ErrBusy
	if msg := refused(t, call(t, tools, conv, "start_errand", `{"site":"example.com","action":"Cancel a visit","details":[],"appointment":0}`)); !strings.Contains(msg, "already working") {
		t.Fatalf("refusal %q", msg)
	}
}

func TestAFailureIsSaidInPlainWords(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	f.startErr = errors.New("sqlite: SQL logic error: no such table: sites (1)")
	args := `{"site":"example.com","action":"Cancel a visit","details":[],"appointment":0}`
	if msg := refused(t, call(t, tools, conv, "start_errand", args)); msg != "something went wrong" {
		t.Fatalf("an internal failure said %q", msg)
	}
	f.startErr = fmt.Errorf("%w: only a change or a cancel names an appointment", signins.ErrBadErrand)
	if msg := refused(t, call(t, tools, conv, "start_errand", args)); !strings.HasSuffix(msg, "only a change or a cancel names an appointment") {
		t.Fatalf("the service's own words: %q", msg)
	}
	f.startErr = fmt.Errorf("%w: secretd refused %q: status 500", secrets.ErrUnavailable, "signins/example.com/password")
	if msg := refused(t, call(t, tools, conv, "start_errand", args)); msg != "secrets unavailable" {
		t.Fatalf("secretd away said %q", msg)
	}
	f.startErr = nil
	if err := s.LinkRun(context.Background(), conv, 41); err != nil {
		t.Fatal(err)
	}
	if msg := refused(t, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"x","owner_event":0}`)); msg != "run 41 is not waiting for an answer" {
		t.Fatalf("not waiting: %q", msg)
	}
}

// confirmEvent is the errand's Confirm, its wait wait.
func confirmEvent(run, wait int64, text string) *agentv1.Event {
	return &agentv1.Event{Kind: &agentv1.Event_Errand{Errand: &agentv1.ErrandEvent{Run: run, Kind: "confirm", Text: text, Wait: wait}}}
}

// told appends the confirm and marks it told, as the hub does once the owner was sent it.
func told(t *testing.T, s *Store, conv int64, e *agentv1.Event) int64 {
	t.Helper()
	id := appendEvent(t, s, conv, e)
	if err := s.tellConfirm(context.Background(), conv, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAnUntoldConfirmIsNeverAnswerable(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	if err := s.LinkRun(context.Background(), conv, 41); err != nil {
		t.Fatal(err)
	}
	appendEvent(t, s, conv, confirmEvent(41, 1, "Confirm: go?")) // kept, but the owner was never sent it
	f.wait(41, "confirm", 1)
	yes := appendEvent(t, s, conv, owner("yes"))
	refusedSaying(t, `not told`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(yes)+`}`))
	if len(f.answers[41]) != 0 {
		t.Fatalf("answered %v", f.answers)
	}
}

func TestOnlyAMessageAfterTheTellingAnswersAConfirm(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	if err := s.LinkRun(context.Background(), conv, 41); err != nil {
		t.Fatal(err)
	}
	confirm := appendEvent(t, s, conv, confirmEvent(41, 1, "Confirm: go?"))
	early := appendEvent(t, s, conv, owner("yes")) // came while the Confirm was being sent
	if err := s.tellConfirm(context.Background(), conv, confirm); err != nil {
		t.Fatal(err)
	}
	f.wait(41, "confirm", 1)
	refusedSaying(t, `before the Confirm`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(early)+`}`))
	later := appendEvent(t, s, conv, owner("yes, go"))
	if m := call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(later)+`}`); m["error"] != nil {
		t.Fatalf("refused: %v", m)
	}
}

// confirmAt fills a conversation up to a confirm for run 41 at event #10 (wait 1), the run waiting on it.
func confirmAt(t *testing.T, f *fakeErrands, s *Store, conv int64) {
	t.Helper()
	for i := 1; i < 10; i++ {
		appendEvent(t, s, conv, owner("…")) // the owner's words, but before the Confirm
	}
	if err := s.LinkRun(context.Background(), conv, 41); err != nil {
		t.Fatal(err)
	}
	if id := told(t, s, conv, confirmEvent(41, 1, "Confirm: Fri 21:00 for 4?")); id != 10 {
		t.Fatalf("the confirm is #%d", id)
	}
	f.wait(41, "confirm", 1)
}

func TestConfirmTakesOnlyALaterOwnerMessage(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	confirmAt(t, f, s, conv)
	if id := appendEvent(t, s, conv, owner("yes, book it")); id != 11 {
		t.Fatalf("the owner's answer is #%d", id)
	}

	refusedSaying(t, `before the Confirm`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":9}`))
	refusedSaying(t, `no answer of yours`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"yes","owner_event":0}`))
	refusedSaying(t, `no answer of yours`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"yes","owner_event":11}`))
	if len(f.answers[41]) != 0 {
		t.Fatalf("answered %v", f.answers)
	}
	if m := call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":11}`); m["error"] != nil {
		t.Fatalf("refused: %v", m)
	}
	if got := f.answers[41]; len(got) != 1 || got[0] != "yes, book it" {
		t.Fatalf("answers %q", got)
	}
}

func TestConfirmIsAnsweredOnce(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	confirmAt(t, f, s, conv)
	appendEvent(t, s, conv, owner("yes"))
	appendEvent(t, s, conv, owner("yes again"))
	call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":11}`)
	// The run still says confirm (the next wait is not told yet): the answered one is not open again.
	refusedSaying(t, `answered already`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":12}`))
	if got := f.answers[41]; len(got) != 1 {
		t.Fatalf("answers %q", got)
	}
}

func TestAnAnsweredConfirmAnswersTheOnesItStoodFor(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	confirmAt(t, f, s, conv)                                 // #10, never answered
	told(t, s, conv, confirmEvent(41, 2, "Confirm: again?")) // #11, the newest
	f.wait(41, "confirm", 2)
	yes := appendEvent(t, s, conv, owner("yes"))
	call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(yes)+`}`)
	again := appendEvent(t, s, conv, owner("yes again"))
	refusedSaying(t, `answered already`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(again)+`}`))
	if got := f.answers[41]; len(got) != 1 {
		t.Fatalf("answers %q", got)
	}
}

func TestConfirmRefusesWhatIsNotTheOwnersWordsHere(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	confirmAt(t, f, s, conv)
	other := open(t, s, "other chat")
	foreign := appendEvent(t, s, other, owner("yes")) // #11, another conversation's
	agentWords := appendEvent(t, s, conv, said("yes"))
	toolResult := appendEvent(t, s, conv, &agentv1.Event{Kind: &agentv1.Event_ToolResult{ToolResult: &agentv1.ToolResult{CallId: "c", ResultJson: `"yes"`}}})
	for _, id := range []int64{foreign, agentWords, toolResult, 999} {
		refusedSaying(t, `not a message of the owner's`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(id)+`}`))
	}
	if len(f.answers[41]) != 0 {
		t.Fatalf("answered %v", f.answers)
	}
}

func TestConfirmNeedsTheConfirmToldInThisConversation(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	if err := s.LinkRun(context.Background(), conv, 41); err != nil {
		t.Fatal(err)
	}
	f.wait(41, "confirm", 1) // the run waits, but the conversation was never told
	yes := appendEvent(t, s, conv, owner("yes"))
	refusedSaying(t, `not told`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(yes)+`}`))
	if len(f.answers[41]) != 0 {
		t.Fatalf("answered %v", f.answers)
	}
}

func TestOneOwnerMessageAnswersOneConfirm(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	confirmAt(t, f, s, conv)
	if err := s.LinkRun(context.Background(), conv, 42); err != nil {
		t.Fatal(err)
	}
	told(t, s, conv, confirmEvent(42, 2, "Confirm: Sat?"))
	f.wait(42, "confirm", 2)
	yes := appendEvent(t, s, conv, owner("yes"))
	call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(yes)+`}`)
	refusedSaying(t, `answered a Confirm already`, call(t, tools, conv, "answer_errand", `{"run":42,"answer":"","owner_event":`+itoa(yes)+`}`))
	if len(f.answers[41]) != 1 || len(f.answers[42]) != 0 {
		t.Fatalf("answers %v", f.answers)
	}
}

func TestAConfirmToldIsTheOneAnswered(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	confirmAt(t, f, s, conv) // #10, wait 1, left unanswered here (answered in the console, say)
	f.wait(41, "confirm", 2) // a new Confirm the conversation is not told yet
	yes := appendEvent(t, s, conv, owner("sure"))
	refusedSaying(t, `no longer the one`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(yes)+`}`))
	if got := f.answered(41); len(got) != 0 {
		t.Fatalf("answered %q", got)
	}
}

func TestAWaitThatChangesBeforeTheAnswerIsRefused(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	confirmAt(t, f, s, conv)
	yes := appendEvent(t, s, conv, owner("yes"))
	f.beforeAnswer = func() { f.questions[41] = &signins.Question{Text: "Confirm: other?", Wait: 2}; f.beforeAnswer = nil }
	refusedSaying(t, `now waits on something else`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(yes)+`}`))
	if got := f.answered(41); len(got) != 0 {
		t.Fatalf("answered %q", got)
	}
	// The claim was let go: with the told Confirm current again, the same message answers it.
	f.wait(41, "confirm", 1)
	if m := call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(yes)+`}`); m["error"] != nil {
		t.Fatalf("refused after the wait changed: %v", m)
	}

	// A question likewise: the wait it checked, or nothing.
	f.wait(41, "question", 3)
	f.beforeAnswer = func() {
		f.questions[41] = &signins.Question{Text: "Confirm: now?", Wait: 4}
		f.waiting[41] = "confirm"
		f.beforeAnswer = nil
	}
	refusedSaying(t, `now waits on something else`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"21:00","owner_event":0}`))
	if got := f.answered(41); len(got) != 1 {
		t.Fatalf("answered %q", got)
	}
}

func TestTwoAnswersRacingOnAConfirmAnswerItOnce(t *testing.T) {
	for range 20 {
		f, s := newFake(), openStore(t)
		tools, conv := NewTools(f, s), open(t, s, "chat")
		confirmAt(t, f, s, conv)
		first, second := appendEvent(t, s, conv, owner("yes")), appendEvent(t, s, conv, owner("yes!"))
		results := make([]string, 2)
		var wg sync.WaitGroup
		for i, id := range []int64{first, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = tools.Call(context.Background(), conv, 0, &agentv1.ToolCall{Name: "answer_errand",
					ArgumentsJson: `{"run":41,"answer":"","owner_event":` + itoa(id) + `}`})
			}()
		}
		wg.Wait()
		if got := f.answered(41); len(got) != 1 {
			t.Fatalf("answered %q: %v", got, results)
		}
		if errs := strings.Count(results[0]+results[1], `"error"`); errs != 1 {
			t.Fatalf("results %v", results)
		}
	}
}

func TestAQuestionTakesTheAgentsAnswer(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	if err := s.LinkRun(context.Background(), conv, 41); err != nil {
		t.Fatal(err)
	}
	f.wait(41, "question", 5)
	if m := call(t, tools, conv, "answer_errand", `{"run":41,"answer":"21:00","owner_event":0}`); m["error"] != nil {
		t.Fatalf("refused: %v", m)
	}
	words := appendEvent(t, s, conv, owner("make it four"))
	if m := call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":`+itoa(words)+`}`); m["error"] != nil {
		t.Fatalf("refused: %v", m)
	}
	if got := f.answers[41]; len(got) != 2 || got[0] != "21:00" || got[1] != "make it four" {
		t.Fatalf("answers %q", got)
	}
	refusedSaying(t, `not both`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"x","owner_event":`+itoa(words)+`}`))
	refusedSaying(t, `not neither`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"","owner_event":0}`))
}

func TestErrandToolsActOnlyOnThisConversationsRuns(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	other := open(t, s, "other")
	if err := s.LinkRun(context.Background(), other, 41); err != nil {
		t.Fatal(err)
	}
	f.wait(41, "question", 5)
	f.runs[41] = signins.AgentRunDetail{}
	refusedSaying(t, `not an errand of this conversation`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"yes","owner_event":0}`))
	refusedSaying(t, `not an errand of this conversation`, call(t, tools, conv, "stop_errand", `{"run":41}`))
	refusedSaying(t, `not an errand of this conversation`, call(t, tools, conv, "errand_status", `{"run":41}`))
	refusedSaying(t, `not an errand of this conversation`, call(t, tools, conv, "answer_errand", `{"run":77,"answer":"yes","owner_event":0}`))
	if len(f.answers) != 0 || len(f.stopped) != 0 {
		t.Fatalf("acted: %v %v", f.answers, f.stopped)
	}
}

func TestAnswerRefusesARunNotWaiting(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	if err := s.LinkRun(context.Background(), conv, 41); err != nil {
		t.Fatal(err)
	}
	refusedSaying(t, `not waiting`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"yes","owner_event":0}`))
	f.wait(41, "human_check", 0)
	refusedSaying(t, `human check`, call(t, tools, conv, "answer_errand", `{"run":41,"answer":"yes","owner_event":0}`))
	if len(f.answers) != 0 {
		t.Fatalf("answered %v", f.answers)
	}
}

func TestStopAndStatusOfAnErrand(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	if err := s.LinkRun(context.Background(), conv, 41); err != nil {
		t.Fatal(err)
	}
	d := signins.AgentRunDetail{Report: "nothing yet"}
	d.State = "running"
	f.runs[41] = d
	f.waiting[41] = "question"
	f.questions[41] = &signins.Question{Text: "Which day?", Options: []string{"Fri", "Sat"}}
	out := tools.Call(context.Background(), conv, 0, &agentv1.ToolCall{Name: "errand_status", ArgumentsJson: `{"run":41}`})
	if want := `{"state":"running","waiting":"question","question":"Which day?","options":["Fri","Sat"],"report":"nothing yet"}`; out != want {
		t.Fatalf("status %s", out)
	}
	if m := call(t, tools, conv, "stop_errand", `{"run":41}`); m["error"] != nil || len(f.stopped) != 1 {
		t.Fatalf("stop: %v %v", m, f.stopped)
	}
}

func TestAppointmentTools(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	f.appts = []signins.Appointment{
		{ID: 3, Site: "example.com", Title: "Visit", Date: "2026-11-10", Time: "15:00", Place: "Main St 1", Status: "upcoming"},
		{ID: 2, Site: "example.com", Title: "Old visit", Status: "cancelled"},
	}
	out := tools.Call(context.Background(), conv, 0, &agentv1.ToolCall{Name: "list_appointments", ArgumentsJson: `{}`})
	want := `{"appointments":[{"id":3,"site":"example.com","title":"Visit","date":"2026-11-10","time":"15:00","place":"Main St 1","status":"upcoming"},` +
		`{"id":2,"site":"example.com","title":"Old visit","date":"","time":"","place":"","status":"cancelled"}]}`
	if out != want {
		t.Fatalf("appointments %s", out)
	}
	m := call(t, tools, conv, "check_appointment", `{"id":3}`)
	if m["run"] != float64(41) || len(f.checked) != 1 || f.checked[0] != 3 {
		t.Fatalf("check: %v %v", m, f.checked)
	}
	if got, ok, _ := s.ConversationOfRun(context.Background(), 41); !ok || got != conv {
		t.Fatal("the check's run is not this conversation's")
	}
}

func TestArgumentsAreParsedStrictly(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	for _, c := range []struct{ name, args, says string }{
		{"stop_errand", `{"run":"41"}`, "run"},
		{"stop_errand", `{"run":4.5}`, "run"},
		{"stop_errand", `{"run":null}`, "run"},
		{"stop_errand", `{}`, "run"},
		{"stop_errand", `{"run":41,"why":"x"}`, "why"},
		{"stop_errand", `[41]`, "object"},
		{"stop_errand", `{"run":41} {}`, "object"},
		{"stop_errand", ``, "object"},
		{"start_errand", `{"site":"example.com","action":"Book a table","details":{"When":"Fri"},"appointment":0}`, "details"},
		{"start_errand", `{"site":"example.com","action":"Book a table","details":[{"name":"When"}],"appointment":0}`, "value"},
		{"start_errand", `{"site":"example.com","action":"Book a table","details":[],"appointment":0,"link":"x"}`, "link"},
		{"list_accounts", `{"all":true}`, "all"},
		{"no_such_tool", `{}`, "no_such_tool"},
	} {
		msg := refused(t, call(t, tools, conv, c.name, c.args))
		if !strings.Contains(msg, c.says) {
			t.Errorf("%s %s: %q does not name %q", c.name, c.args, msg, c.says)
		}
	}
	if len(f.stopped) != 0 || len(f.started) != 0 {
		t.Fatalf("acted on a bad call: %v %v", f.stopped, f.started)
	}
	if out := tools.Call(context.Background(), conv, 0, nil); !strings.Contains(out, "error") {
		t.Fatalf("a nil call: %s", out)
	}
}

func TestSpecsAreStrictAndGeneral(t *testing.T) {
	tools := NewTools(newFake(), openStore(t))
	var names []string
	for _, spec := range tools.Specs() {
		names = append(names, spec.GetName())
		var schema map[string]any
		if err := json.Unmarshal([]byte(spec.GetParametersJson()), &schema); err != nil {
			t.Fatalf("%s: %v", spec.GetName(), err)
		}
		strict(t, spec.GetName(), schema)
		if regexp.MustCompile(`(?i)\b(book|reserv|service|table|room)`).MatchString(spec.GetDescription()) {
			t.Errorf("%s: the description names an action: %s", spec.GetName(), spec.GetDescription())
		}
	}
	want := "list_accounts start_errand answer_errand stop_errand errand_status list_appointments check_appointment"
	if strings.Join(names, " ") != want {
		t.Fatalf("tools %v", names)
	}
}

// strict checks a schema is strict-mode's: every object lists all its properties as required and
// takes no others.
func strict(t *testing.T, at string, schema map[string]any) {
	t.Helper()
	switch schema["type"] {
	case "object":
		props, _ := schema["properties"].(map[string]any)
		if props == nil || schema["additionalProperties"] != false {
			t.Errorf("%s: an object open to more: %v", at, schema)
		}
		required := map[string]bool{}
		for _, r := range schema["required"].([]any) {
			required[r.(string)] = true
		}
		if len(required) != len(props) {
			t.Errorf("%s: required %v of %v", at, schema["required"], props)
		}
		for name, p := range props {
			if !required[name] {
				t.Errorf("%s.%s is not required", at, name)
			}
			strict(t, at+"."+name, p.(map[string]any))
		}
	case "array":
		strict(t, at+"[]", schema["items"].(map[string]any))
	case "string", "integer":
	default:
		t.Errorf("%s: type %v", at, schema["type"])
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestAnOwnerMessageNotShownYetAnswersNothing(t *testing.T) {
	f, s := newFake(), openStore(t)
	tools, conv := NewTools(f, s), open(t, s, "chat")
	confirmAt(t, f, s, conv)
	taking, err := s.Intake(context.Background(), conv, owner("what is it?"), t0) // #11, this turn's
	if err != nil {
		t.Fatal(err)
	}
	later, err := s.Intake(context.Background(), conv, owner("yes"), t0) // #12, queued for its own turn
	if err != nil {
		t.Fatal(err)
	}
	answer := func(owner int64) map[string]any {
		out := tools.Call(context.Background(), conv, taking, &agentv1.ToolCall{Name: "answer_errand",
			ArgumentsJson: `{"run":41,"answer":"","owner_event":` + itoa(owner) + `}`})
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	refusedSaying(t, `not shown`, answer(later))
	f.wait(41, "question", 2)
	refusedSaying(t, `not shown`, answer(later))
	if len(f.answers[41]) != 0 {
		t.Fatalf("answered %v", f.answers)
	}
	// The turn's own message was shown.
	if m := answer(taking); m["error"] != nil {
		t.Fatalf("refused the turn's own message: %v", m)
	}
}
