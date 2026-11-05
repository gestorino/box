package calls

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"concierge/engine/internal/hostsettings"
)

// dialModem is a fake Modem for placing calls: after Dial, each List shows the call in the next
// state of script (the last one sticks); every Dial, Hangup and audio step lands in one log, in order.
type dialModem struct {
	fakeModem
	log     *callLog
	script  []ModemCall // State and Reason per List after the dial
	step    int
	dialed  bool
	dialErr error
}

type callLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *callLog) add(s string) { l.mu.Lock(); l.steps = append(l.steps, s); l.mu.Unlock() }
func (l *callLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.steps...)
}

func (d *dialModem) Dial(_ context.Context, number string) (string, string, error) {
	if d.dialErr != nil {
		return "", "", d.dialErr
	}
	d.log.add("dial " + number)
	d.mu.Lock()
	d.dialed = true
	d.mu.Unlock()
	return ":1.10", "/Call/1", nil
}

func (d *dialModem) List(context.Context) (string, []ModemCall, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.dialed || len(d.script) == 0 {
		return ":1.10", nil, nil
	}
	c := d.script[min(d.step, len(d.script)-1)]
	d.step++
	c.Path, c.Direction = "/Call/1", "outgoing"
	return ":1.10", []ModemCall{c}, nil
}

func (d *dialModem) Hangup(_ context.Context, _, path string) error {
	d.log.add("hangup " + path)
	return nil
}

type fakeAudio struct {
	log      *callLog
	routeErr error
}

func (a *fakeAudio) Route(context.Context) error   { a.log.add("route"); return a.routeErr }
func (a *fakeAudio) Unroute(context.Context) error { a.log.add("unroute"); return nil }

// neverAnswered is hostd before its first answer: nothing extra is kept.
func neverAnswered() (hostsettings.CallsKeep, bool) { return hostsettings.CallsKeep{}, false }

func st(state, reason string) ModemCall { return ModemCall{State: state, Reason: reason} }

func TestCallerHoldsItsCallUntilDone(t *testing.T) {
	log := &callLog{}
	m := &dialModem{log: log, script: []ModemCall{st("ringing-out", ""), st("terminated", "refused-or-busy")}}
	hold := func(path string) func() { log.add("hold " + path); return func() { log.add("release " + path) } }
	c := NewCaller(m, &fakeAudio{log: log}, &fakeDuplex{log: log}, hold, func(string, Kept) {}, neverAnswered, "", 50*time.Millisecond, 2*time.Millisecond)
	if out, _ := c.Converse(ctx, "+351911111111", opener(newTalker(nil)), waitScript, held); out != "busy" {
		t.Fatalf("%q", out)
	}
	want := []string{"dial +351911111111", "hold /Call/1", "hangup /Call/1", "release /Call/1"}
	if got := log.get(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestCallNotAnswered(t *testing.T) {
	c, m, _, _ := converser(t, st("ringing-out", ""))
	out, err := c.Converse(ctx, "+351912345678", opener(newTalker(nil)), waitScript, held)
	if err != nil || out != "no-answer" {
		t.Fatalf("%q %v", out, err)
	}
	if got := m.log.get(); !reflect.DeepEqual(got, []string{"dial +351912345678", "hangup /Call/1"}) {
		t.Fatalf("%v", got)
	}
}

func TestCallEndedBeforeAnswer(t *testing.T) {
	for reason, want := range map[string]string{"refused-or-busy": "busy", "error": "failed", "terminated": "no-answer"} {
		c, m, _, _ := converser(t, st("ringing-out", ""), st("terminated", reason))
		out, err := c.Converse(ctx, "+351912345678", opener(newTalker(nil)), waitScript, held)
		if err != nil || out != want {
			t.Fatalf("%s: %q %v", reason, out, err)
		}
		for _, s := range m.log.get() {
			if s == "route" {
				t.Fatalf("%s: routed audio for an unanswered call", reason)
			}
		}
	}
}

// Routing that fails halfway is still unrouted, and the call hung up.
func TestARouteThatFailsIsUnroutedAndHungUp(t *testing.T) {
	log := &callLog{}
	m := &dialModem{log: log, script: []ModemCall{st("active", "accepted")}}
	c := NewCaller(m, &fakeAudio{log: log, routeErr: errors.New("ERROR")}, &fakeDuplex{log: log}, func(string) func() { return func() {} }, func(string, Kept) {}, neverAnswered, "", 50*time.Millisecond, 2*time.Millisecond)
	if out, err := c.Converse(ctx, "+351912345678", opener(newTalker(nil)), waitScript, held); err != nil || out != "failed" {
		t.Fatalf("%q %v", out, err)
	}
	want := []string{"dial +351912345678", "route", "unroute", "hangup /Call/1"}
	if got := log.get(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}
