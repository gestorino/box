package signins

import (
	"context"
	"testing"
	"time"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/listen/listentest"
)

// smsFrom is an incoming SMS as smsd hands it to a listener.
func smsFrom(number, text string) *listenv1.Message {
	return &listenv1.Message{Body: &listenv1.Message_Sms{Sms: &listenv1.SMS{Number: number, Text: text}}}
}

// serveSMS is smsd's listen service on a socket, and the engine's client of it.
func serveSMS(t *testing.T) (*listentest.Fake, listen.Listener) {
	t.Helper()
	fake := listentest.NewFake(listen.MatchSMS)
	return fake, listen.NewClient(listentest.Serve(t, fake).Socket)
}

// openTestCodes opens a code wait that ends with the test.
func openTestCodes(t *testing.T, sms listen.Listener) *codeWait {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return openCodes(ctx, sms, caughtUpWait)
}

// buffered waits until the wait holds n messages: an Add reaches it over the socket.
func buffered(t *testing.T, w *codeWait, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		w.mu.Lock()
		got := len(w.msgs)
		w.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("buffered %d messages, want %d", got, n)
		}
	}
}

func TestNothingStoredBeforeTheFlowCounts(t *testing.T) {
	fake, sms := serveSMS(t)
	fake.Add(smsFrom("Booksy", "Code 482913"))
	w := openTestCodes(t, sms)
	if code, ok := w.Take(context.Background(), 50*time.Millisecond); ok || code != "" {
		t.Fatalf("code = %q, %v", code, ok)
	}
}

func TestTheNewestCodeWins(t *testing.T) {
	fake, sms := serveSMS(t)
	w := openTestCodes(t, sms)
	fake.Add(smsFrom("Booksy", "Code 111111"))
	fake.Add(smsFrom("Booksy", "Code 222222")) // a resend
	buffered(t, w, 2)
	code, ok := w.Take(context.Background(), 5*time.Second)
	if !ok || code != "222222" {
		t.Fatalf("code = %q, %v", code, ok)
	}
}

func TestALaterStepNeverReusesAMessage(t *testing.T) {
	fake, sms := serveSMS(t)
	w := openTestCodes(t, sms)
	fake.Add(smsFrom("Booksy", "Code 111111"))
	fake.Add(smsFrom("Booksy", "Code 222222"))
	buffered(t, w, 2)
	if code, ok := w.Take(context.Background(), 5*time.Second); !ok || code != "222222" {
		t.Fatalf("first step: code = %q, %v", code, ok)
	}
	if code, ok := w.Take(context.Background(), 50*time.Millisecond); ok {
		t.Fatalf("second step took %q from the first step's messages", code)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		fake.Add(smsFrom("Booksy", "Code 333333"))
	}()
	if code, ok := w.Take(context.Background(), 5*time.Second); !ok || code != "333333" {
		t.Fatalf("second step: code = %q, %v", code, ok)
	}
}

// A code is a run of 4 to 8 digits, whoever sent it: a site's sender is unknown without a
// description. A longer run (an order number) or a shorter one is no code.
func TestACodeIsFourToEightDigitsFromAnySender(t *testing.T) {
	fake, sms := serveSMS(t)
	w := openTestCodes(t, sms)
	for text, want := range map[string]string{"Your code is 4821": "4821", "Code: 48291377": "48291377",
		"Ref 123456789, code 552211": "552211", "Saldo 100 pontos": ""} {
		fake.Add(smsFrom("ANYONE", text))
		buffered(t, w, 1)
		code, ok := w.Take(context.Background(), 50*time.Millisecond)
		if code != want || ok != (want != "") {
			t.Errorf("%q: code = %q, %v", text, code, ok)
		}
		w.mu.Lock()
		w.msgs = nil
		w.mu.Unlock()
	}
}

// Ending the flow's ctx ends its listener: smsd forgets it.
func TestTheCodeListenerEndsWithItsContext(t *testing.T) {
	fake, sms := serveSMS(t)
	ctx, cancel := context.WithCancel(context.Background())
	openCodes(ctx, sms, caughtUpWait)
	if n := fake.Listeners(); n != 1 {
		t.Fatalf("listeners while open = %d", n)
	}
	cancel()
	for deadline := time.Now().Add(5 * time.Second); fake.Listeners() != 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("listeners after the flow = %d", fake.Listeners())
		}
	}
}

// silent is a connector that never answers: no caught_up, no message.
type silent struct{}

func (silent) Listen(context.Context, *listenv1.ListenRequest) <-chan *listenv1.Event {
	return make(chan *listenv1.Event)
}

// Opening waits for caught_up only while ctx lives: an engine shutting down isn't held up.
func TestOpeningCodesEndsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	openCodes(ctx, silent{}, caughtUpWait)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("opened after %v", d)
	}
}
