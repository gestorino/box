package signins

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"concierge/engine/internal/browser"
)

// errandFixture is shop.test's card with the box's account on it in state ("" for none).
func errandFixture(t *testing.T, state string) *fixture {
	t.Helper()
	f := newFixture(t)
	if state != "" {
		f.signedUp(t, state)
	}
	return f
}

func (f *fixture) errand(t *testing.T, link string) int64 {
	t.Helper()
	id, err := f.svc.StartErrand(context.Background(), Errand{URL: link, Action: "book a table"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) runDetail(t *testing.T, id int64) AgentRunDetail {
	t.Helper()
	d, found, err := f.svc.AgentRun(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("errand %d: %v, %v", id, found, err)
	}
	return d
}

// soon waits for cond, failing the test after a few seconds.
func soon(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
	}
}

func hasLine(lines []RunLine, kind, text string) bool {
	for _, l := range lines {
		if l.Kind == kind && strings.Contains(l.Text, text) {
			return true
		}
	}
	return false
}

func asksQuestion(got chan<- answer) func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
	return func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		v, ok := ask(ctx, browser.AskRequest{Name: "question", Why: "Which day?", Options: []string{"Sat", "Sun"}})
		got <- answer{v, ok}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return browser.Message{"outcome": "done", "success": true, "report": "booked"}, nil
	}
}

// Run 51: a deploy restarted the engine mid-errand, and the errand said the browser had failed.
func TestAnErrandCutShortByTheBoxStoppingSaysSo(t *testing.T) {
	f := errandFixture(t, "registered")
	base, stop := context.WithCancel(context.Background())
	f.svc.cfg.Base = base
	f.worker.onAgent = asksQuestion(make(chan answer, 1))
	id := f.errand(t, "https://shop.test/a")
	soon(t, "the question is held", func() bool { return f.runDetail(t, id).Question != nil })
	stop()
	f.svc.runs.Wait()
	if d := f.runDetail(t, id); d.State != "ended" || d.Reason != "the box restarted" {
		t.Fatalf("errand = %s %q", d.State, d.Reason)
	}
}

func TestAnErrandOnASignedInSiteUsesItsProfile(t *testing.T) {
	f := errandFixture(t, "registered")
	id := f.errand(t, "https://shop.test/place/1")
	f.svc.runs.Wait()

	if len(f.worker.agentRuns) != 1 {
		t.Fatalf("errands sent = %d", len(f.worker.agentRuns))
	}
	body := f.worker.agentRuns[0]
	if body["site"] != shopKey || body["session"] != shopKey || body["url"] != "https://shop.test/place/1" || body["action"] != "book a table" ||
		body["kind"] != "errand" {
		t.Fatalf("body = %v", body)
	}
	if !reflect.DeepEqual(body["allowed_domains"], []string{shopKey, "*." + shopKey}) {
		t.Fatalf("allowed_domains = %v", body["allowed_domains"])
	}
	if body["run"] != id || body["part"] != 1 {
		t.Fatalf("not recorded: %v", body)
	}
	views, err := f.svc.AgentRuns(context.Background())
	if err != nil || len(views) != 1 {
		t.Fatalf("errands = %v, %v", views, err)
	}
	if v := views[0]; v.ID != id || v.Kind != "errand" || v.URL != "https://shop.test/place/1" || v.Action != "book a table" {
		t.Fatalf("view = %+v", v)
	}
}

func TestAnUnknownHostRunsInAThrowawayProfile(t *testing.T) {
	f := errandFixture(t, "registered")
	f.errand(t, "https://www.new-place.test/a")
	f.svc.runs.Wait()

	body := f.worker.agentRuns[0]
	if body["site"] != "new-place.test" || body["session"] != "" || body["values"] != nil {
		t.Fatalf("body = %v", body)
	}
	if !reflect.DeepEqual(body["allowed_domains"], []string{"new-place.test", "*.new-place.test"}) {
		t.Fatalf("allowed_domains = %v", body["allowed_domains"])
	}
}

func TestAnErrandNeedsALinkAndAnAction(t *testing.T) {
	f := errandFixture(t, "registered")
	for _, c := range [][2]string{{"ftp://shop.test/a", "book"}, {"https://shop.test/a", "  "}, {"not a link", "book"}} {
		if _, err := f.svc.StartErrand(context.Background(), Errand{URL: c[0], Action: c[1]}); !errors.Is(err, ErrBadErrand) {
			t.Fatalf("%v: err = %v", c, err)
		}
	}
}

func TestAQuestionWaitsForTheOwner(t *testing.T) {
	f := errandFixture(t, "registered")
	got := make(chan answer, 1)
	f.worker.onAgent = asksQuestion(got)
	id := f.errand(t, "https://shop.test/a")
	soon(t, "the question is held", func() bool { return f.runDetail(t, id).Question != nil })

	d := f.runDetail(t, id)
	if d.Waiting != "question" || d.Question.Text != "Which day?" || !reflect.DeepEqual(d.Question.Options, []string{"Sat", "Sun"}) {
		t.Fatalf("detail = %+v %+v", d.AgentRunView, d.Question)
	}
	// The dialog shows the question as the box's message; its choices are the question's own.
	if !slices.ContainsFunc(d.Lines, func(l RunLine) bool { return l.Kind == "question" && l.Text == "Which day?" }) {
		t.Fatalf("lines = %v", d.Lines)
	}
	if err := f.svc.Answer(id, "Sat"); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	if a := <-got; a != (answer{"Sat", true}) {
		t.Fatalf("worker got %v", a)
	}
	d = f.runDetail(t, id)
	if d.Question != nil || !hasLine(d.Lines, "reply", "Sat") || d.Report != "booked" {
		t.Fatalf("after: %+v %v %q", d.Question, d.Lines, d.Report)
	}
}

func TestAnUnansweredQuestionEndsTheRun(t *testing.T) {
	f := errandFixture(t, "registered")
	h := f.hookWaits()
	got := make(chan answer, 1)
	f.worker.onAgent = asksQuestion(got)
	id := f.errand(t, "https://shop.test/a")
	if d := <-h.opened; d != time.Hour {
		t.Fatalf("question waits %v", d)
	}
	h.expire <- testNow
	f.svc.runs.Wait()
	if a := <-got; a.ok {
		t.Fatalf("worker got %v", a)
	}
	if d := f.runDetail(t, id); d.State != "ended" || d.Reason != "no answer" || d.Question != nil {
		t.Fatalf("errand = %s %q %+v", d.State, d.Reason, d.Question)
	}
}

func TestAHumanCheckWaitsLikeASignIn(t *testing.T) {
	f := errandFixture(t, "registered")
	got := make(chan answer, 1)
	f.worker.onAgent = func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		v, ok := ask(ctx, browser.AskRequest{Name: "human_check", Why: "a slider"})
		got <- answer{v, ok}
		return browser.Message{"outcome": "done", "success": true, "report": "booked"}, nil
	}
	id := f.errand(t, "https://shop.test/a")
	soon(t, "the captcha wait", func() bool { return f.runDetail(t, id).Waiting == "captcha" })
	if err := f.svc.Done(shopKey); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	if a := <-got; a != (answer{"done", true}) {
		t.Fatalf("worker got %v", a)
	}
}

func TestStopEndsTheRun(t *testing.T) {
	f := errandFixture(t, "registered")
	got := make(chan answer, 1)
	f.worker.onAgent = asksQuestion(got)
	id := f.errand(t, "https://shop.test/a")
	soon(t, "the question is held", func() bool { return f.runDetail(t, id).Question != nil })
	if err := f.svc.Stop(id); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	if d := f.runDetail(t, id); d.State != "ended" || d.Reason != "stopped" {
		t.Fatalf("errand = %s %q", d.State, d.Reason)
	}
	if err := f.svc.Answer(id, "Sat"); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("a late answer: %v", err)
	}
	if err := f.svc.Stop(id); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("a late stop: %v", err)
	}
}

func TestGivingUpACaptchaEndsTheErrand(t *testing.T) {
	f := errandFixture(t, "registered")
	got := make(chan answer, 1)
	f.worker.onAgent = func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		v, ok := ask(ctx, browser.AskRequest{Name: "human_check", Why: "a slider"})
		got <- answer{v, ok}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return browser.Message{"outcome": "done", "success": false, "report": "stuck"}, nil
	}
	id := f.errand(t, "https://shop.test/a")
	soon(t, "the captcha wait", func() bool { return f.runDetail(t, id).Waiting == "captcha" })
	if err := f.svc.GiveUp(shopKey); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	if a := <-got; a.ok {
		t.Fatalf("worker got %v", a)
	}
	if d := f.runDetail(t, id); d.State != "ended" || d.Reason != "the captcha wasn't solved" {
		t.Fatalf("errand = %s %q", d.State, d.Reason)
	}
}

func TestAnErrandThatCantBeRecordedEndsItsRun(t *testing.T) {
	f := errandFixture(t, "registered")
	ctx := context.Background()
	var last int64
	f.store.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM runs`).Scan(&last)
	if err := f.store.AddAgentRun(ctx, last+1, "https://other.test/", "x"); err != nil { // an orphan in the way
		t.Fatal(err)
	}
	if _, err := f.svc.StartErrand(ctx, Errand{URL: "https://shop.test/a", Action: "book a table"}); err == nil {
		t.Fatal("started over an orphan row")
	}
	runs, _ := f.store.Runs(ctx, shopKey)
	if len(runs) != 1 || runs[0].State == "running" {
		t.Fatalf("runs = %+v", runs)
	}
}

// Run 50: the owner said "no" at the final question and the errand ended amber, as if it failed.
func TestAnErrandDoneWithoutBookingIsNoWarning(t *testing.T) {
	for _, c := range []struct {
		reply         browser.Message
		state, reason string
	}{
		{browser.Message{"outcome": "done", "success": true, "report": "booked"}, "ended", "done"},
		// Not done says why, in the model's first sentence (run 78: nothing to cancel).
		{browser.Message{"outcome": "done", "success": false, "report": "There are no appointments to cancel. The page says so."},
			"ended", "not done: There are no appointments to cancel."},
		{browser.Message{"outcome": "done", "success": false, "report": ""}, "ended", "not done"},
		{browser.Message{"outcome": "failed", "success": false, "report": ""}, "warn", "the run did not finish"},
		// The worker's reason, when it gave one: a key removed mid-errand.
		{browser.Message{"outcome": "failed", "success": false, "report": "", "error": "no OpenRouter key"}, "warn", "no OpenRouter key"},
	} {
		f := errandFixture(t, "registered")
		f.worker.onAgent = func(context.Context, browser.Message, browser.Ask) (browser.Message, error) { return c.reply, nil }
		id := f.errand(t, "https://shop.test/a")
		f.svc.runs.Wait()
		if d := f.runDetail(t, id); d.State != c.state || d.Reason != c.reason {
			t.Errorf("%v: errand = %s %q", c.reply, d.State, d.Reason)
		}
	}
}

func TestABusySiteRefusesAnErrand(t *testing.T) {
	f := errandFixture(t, "registered")
	f.svc.claim(shopKey, phaseRegistering)
	if _, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/a", Action: "book a table"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v", err)
	}
}

// modelCall is the worker telling one of an errand's model calls as it returns.
func modelCall(model, provider string, cost float64, in, out int) browser.Message {
	return browser.Message{"event": "model_call", "payload": map[string]any{"model": model, "provider": provider,
		"cost_usd": cost, "input_tokens": float64(in), "output_tokens": float64(out)}}
}

const (
	flash = "deepseek/deepseek-v4.1-flash-1017"
	haiku = "anthropic/claude-haiku-5.5"
)

// near is a sum of floats, as the engine adds them.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestTheBilledCostIsTheRunsCost(t *testing.T) {
	f := errandFixture(t, "registered")
	f.worker.progress = []browser.Message{modelCall(flash, "Morph", 0.001, 100, 10),
		modelCall(haiku, "Anthropic", 0.002, 300, 20), modelCall(flash, "Morph", 0.0011, 120, 12)}
	f.worker.onAgent = func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
		return browser.Message{"outcome": "done", "success": true, "report": "Reserva confirmada"}, nil
	}
	id := f.errand(t, "https://shop.test/a")
	f.svc.runs.Wait()
	d := f.runDetail(t, id)
	if d.LLM == nil || !near(d.LLM.CostUSD, 0.0041) || !d.LLM.Priced || d.LLM.Calls != 3 || d.LLM.InputTokens != 520 || d.LLM.Partial {
		t.Fatalf("llm = %+v", d.LLM)
	}
	if len(d.Served) != 2 || d.Served[0].Model != flash || d.Served[0].Provider != "Morph" || d.Served[0].Calls != 2 || !near(d.Served[0].CostUSD, 0.0021) ||
		d.Served[1] != (Served{Model: haiku, Provider: "Anthropic", Calls: 1, CostUSD: 0.002}) || d.Report != "Reserva confirmada" {
		t.Fatalf("served = %+v, report %q", d.Served, d.Report)
	}
}

// Runs 53–55 were stopped, and what their model calls had cost was lost with the reply that never came.
func TestAStoppedErrandKeepsWhatItsModelCallsCost(t *testing.T) {
	f := errandFixture(t, "registered")
	f.worker.progress = []browser.Message{modelCall(flash, "Morph", 0.001, 100, 10)}
	f.worker.onAgent = asksQuestion(make(chan answer, 1))
	id := f.errand(t, "https://shop.test/a")
	soon(t, "the question is held", func() bool { return f.runDetail(t, id).Question != nil })
	if err := f.svc.Stop(id); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	d := f.runDetail(t, id)
	if d.Reason != "stopped" || !reflect.DeepEqual(d.Served, []Served{{Model: flash, Provider: "Morph", Calls: 1, CostUSD: 0.001}}) {
		t.Fatalf("errand = %q, served = %+v", d.Reason, d.Served)
	}
	if d.LLM == nil || d.LLM.Calls != 1 || !near(d.LLM.CostUSD, 0.001) {
		t.Fatalf("llm = %+v", d.LLM)
	}
}

func TestARestartEndsARunningErrand(t *testing.T) {
	f := errandFixture(t, "registered")
	ctx := context.Background()
	id, err := f.store.StartRun(ctx, shopKey, "errand", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddAgentRun(ctx, id, "https://shop.test/a", "book a table"); err != nil {
		t.Fatal(err)
	}
	f.store.Close()
	store, err := OpenStore(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	svc := NewService(Config{Store: store, Worker: f.worker, Now: func() time.Time { return testNow }})
	d, found, err := svc.AgentRun(ctx, id)
	if err != nil || !found {
		t.Fatalf("errand: %v %v", found, err)
	}
	if d.State != "ended" || d.Reason != "the engine restarted" || d.Waiting != "" || d.Question != nil {
		t.Fatalf("errand = %s %q %q", d.State, d.Reason, d.Waiting)
	}
}
