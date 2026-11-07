package signins

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"concierge/engine/internal/browser"
)

// eventLog collects what Config.Events was told.
type eventLog struct {
	mu  sync.Mutex
	got []RunEvent
}

func (l *eventLog) add(e RunEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, e)
}

func (l *eventLog) all() []RunEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]RunEvent(nil), l.got...)
}

func (f *fixture) events() *eventLog {
	l := &eventLog{}
	f.svc.cfg.Events = l.add
	return l
}

func asks(name, why string, options ...string) func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
	return func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		ask(ctx, browser.AskRequest{Name: name, Why: why, Options: options})
		return browser.Message{"outcome": "done", "success": true, "report": "booked"}, nil
	}
}

func TestAQuestionIsAnEvent(t *testing.T) {
	f := errandFixture(t, "registered")
	ev := f.events()
	f.worker.onAgent = asks("question", "Which time?", "19:30", "21:00")
	id := f.errand(t, "https://shop.test/a")
	soon(t, "the question event", func() bool { return len(ev.all()) == 1 })
	want := RunEvent{Run: id, Kind: "question", Text: "Which time?", Options: []string{"19:30", "21:00"}}
	if got := ev.all()[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("event = %+v", got)
	}
	// Told after the question is recorded: Waiting already reports it.
	if kind, q := f.svc.Waiting(id); kind != "question" || q == nil || q.Text != "Which time?" {
		t.Fatalf("waiting = %q %+v", kind, q)
	}
	if err := f.svc.Answer(id, "21:00"); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	if kind, q := f.svc.Waiting(id); kind != "" || q != nil {
		t.Fatalf("waiting after = %q %+v", kind, q)
	}
}

func TestAConfirmIsItsOwnKind(t *testing.T) {
	f := errandFixture(t, "registered")
	ev := f.events()
	f.worker.onAgent = asks("question", "  confirm: book Friday 21:00?", "Yes", "No")
	id := f.errand(t, "https://shop.test/a")
	soon(t, "the confirm event", func() bool { return len(ev.all()) == 1 })
	if got := ev.all()[0]; got.Kind != "confirm" || got.Text != "  confirm: book Friday 21:00?" {
		t.Fatalf("event = %+v", got)
	}
	if kind, _ := f.svc.Waiting(id); kind != "confirm" {
		t.Fatalf("waiting = %q", kind)
	}
	if err := f.svc.Stop(id); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
}

func TestAHumanCheckIsAnEvent(t *testing.T) {
	f := errandFixture(t, "registered")
	ev := f.events()
	f.worker.onAgent = asks("human_check", "a slider")
	id := f.errand(t, "https://shop.test/a")
	soon(t, "the human check event", func() bool { return len(ev.all()) == 1 })
	if got := ev.all()[0]; got.Run != id || got.Kind != "human_check" || got.Text != "a slider" {
		t.Fatalf("event = %+v", got)
	}
	if kind, q := f.svc.Waiting(id); kind != "human_check" || q != nil {
		t.Fatalf("waiting = %q %+v", kind, q)
	}
	if err := f.svc.Done(shopKey); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
}

func TestAnErrandsEndIsAnEvent(t *testing.T) {
	cases := []struct {
		name  string
		reply browser.Message
		err   error
		stop  bool
		want  RunEvent
	}{
		{"done", browser.Message{"outcome": "done", "success": true, "report": "Booked for 21:00."}, nil, false,
			RunEvent{Kind: "done", Text: "Booked for 21:00."}},
		{"not done", browser.Message{"outcome": "done", "success": false, "report": "No tables left. Sorry."}, nil, false,
			RunEvent{Kind: "not_done", Text: "No tables left. Sorry."}},
		{"failed", nil, errors.New("boom"), false,
			RunEvent{Kind: "failed", Text: "the browser could not finish the run"}},
		{"away", nil, browser.ErrUnavailable, false,
			RunEvent{Kind: "failed", Text: "the browser worker was away"}},
		{"stopped", nil, nil, true, RunEvent{Kind: "stopped", Text: "stopped"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := errandFixture(t, "registered")
			ev := f.events()
			f.worker.onAgent = func(ctx context.Context, _ browser.Message, _ browser.Ask) (browser.Message, error) {
				if c.stop {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return c.reply, c.err
			}
			id := f.errand(t, "https://shop.test/a")
			if c.stop {
				soon(t, "the run is in flight", func() bool { return f.svc.Stop(id) == nil })
			}
			f.svc.runs.Wait()
			c.want.Run = id
			if got := ev.all(); !reflect.DeepEqual(got, []RunEvent{c.want}) {
				t.Fatalf("events = %+v", got)
			}
		})
	}
}

func TestAnAgentRunWithNoEventsHookStillRuns(t *testing.T) {
	f := errandFixture(t, "registered")
	f.worker.onAgent = asks("question", "Which time?")
	id := f.errand(t, "https://shop.test/a")
	soon(t, "the question", func() bool { return f.runDetail(t, id).Question != nil })
	if err := f.svc.Stop(id); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
}

func TestConfirmIsRecognised(t *testing.T) {
	for text, want := range map[string]bool{"Confirm: x?": true, " CONFIRM: x": true, "Confirmed?": false, "Which?": false} {
		if got := isConfirm(text); got != want {
			t.Errorf("isConfirm(%q) = %v", text, got)
		}
	}
}
