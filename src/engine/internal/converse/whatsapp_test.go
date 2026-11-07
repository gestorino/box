package converse

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/h2c"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/listen/listentest"
)

// phoneSettings is Settings with a phone and a switch the test moves.
type phoneSettings struct {
	mu       sync.Mutex
	phone    string
	whatsapp bool
}

func (s *phoneSettings) Agent(context.Context) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "claude", s.whatsapp, nil
}
func (s *phoneSettings) OwnerPhone(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phone, nil
}
func (s *phoneSettings) set(phone string, whatsapp bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phone, s.whatsapp = phone, whatsapp
}

type ownerMsg struct{ channel, thread, text string }

type fakeOwners struct{ got chan ownerMsg }

func (f *fakeOwners) Owner(_ context.Context, channel, thread, text string) {
	f.got <- ownerMsg{channel, thread, text}
}

type waRig struct {
	fake     *listentest.Fake
	settings *phoneSettings
	owners   *fakeOwners
}

func newWARig(t *testing.T, phone string, on bool) *waRig {
	t.Helper()
	fake := listentest.NewFake(listen.MatchWhatsApp)
	srv := listentest.Serve(t, fake)
	r := &waRig{fake: fake, settings: &phoneSettings{phone: phone, whatsapp: on},
		owners: &fakeOwners{got: make(chan ownerMsg, 10)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		listenWhatsApp(ctx, listen.NewClient(srv.Socket), r.owners, r.settings, time.Now, 20*time.Millisecond)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

// waitOpen waits until exactly one listener is open. A live-only listener has no replay to count, so
// a test that needs the listener in place before it adds a message uses this and then retries.
func (r *waRig) waitOpen(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.fake.Listeners() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("listeners = %d, want 1", r.fake.Listeners())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// next is the next message the hub got, adding m every 20 ms until it does (a live message added
// before the listener's stream subscribed is lost).
func (r *waRig) next(t *testing.T, m func() *listenv1.Message) ownerMsg {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		r.fake.Add(m())
		select {
		case got := <-r.owners.got:
			return got
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("nothing reached the hub")
		}
	}
}

func wa(phone, text string, sent time.Time) *listenv1.Message {
	return &listenv1.Message{Body: &listenv1.Message_Whatsapp{Whatsapp: &listenv1.WhatsApp{
		Chat: phone + "@s.whatsapp.net", Phone: phone, Text: text, SentAt: timestamppb.New(sent)}}}
}

func TestTheOwnersWhatsAppReachesTheHub(t *testing.T) {
	r := newWARig(t, "+351 91 234 5678", true)
	r.waitOpen(t)
	got := r.next(t, func() *listenv1.Message { return wa("351912345678", "book me a table", time.Now()) })
	if got != (ownerMsg{"whatsapp", "351912345678@s.whatsapp.net", "book me a table"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestWhatsAppOffNothingReachesTheHub(t *testing.T) {
	r := newWARig(t, "+351912345678", false)
	r.waitOpen(t)
	for i := 0; i < 10; i++ {
		r.fake.Add(wa("351912345678", "hello", time.Now()))
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case m := <-r.owners.got:
		t.Fatalf("reached the hub: %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAStaleMessageIsDropped(t *testing.T) {
	r := newWARig(t, "+351912345678", true)
	r.waitOpen(t)
	n := 0
	got := r.next(t, func() *listenv1.Message {
		n++
		if n%2 == 1 {
			return wa("351912345678", "old", time.Now().Add(-11*time.Minute))
		}
		return wa("351912345678", "fresh", time.Now().Add(-9*time.Minute))
	})
	if got.text != "fresh" {
		t.Fatalf("got %+v", got)
	}
}

func TestAChangedContactReopensTheListener(t *testing.T) {
	r := newWARig(t, "+351912345678", true)
	r.waitOpen(t)
	r.settings.set("+351933333333", true)
	got := r.next(t, func() *listenv1.Message { return wa("351933333333", "the new number", time.Now()) })
	if got.text != "the new number" {
		t.Fatalf("got %+v", got)
	}
	r.fake.Add(wa("351912345678", "the old number", time.Now()))
	select {
	case m := <-r.owners.got:
		t.Fatalf("the old number reached the hub: %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// deliver alone: the engine checks the sender, the send time and the listener itself.
func TestDeliverChecksTheMessageItself(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	s := &phoneSettings{whatsapp: true}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	noTime := wa("351912345678", "x", now)
	noTime.GetWhatsapp().SentAt = nil
	for name, c := range map[string]struct {
		ctx context.Context
		m   *listenv1.Message
		ok  bool
	}{
		"the owner's":       {context.Background(), wa("351912345678", "x", now), true},
		"another number":    {context.Background(), wa("351999999999", "x", now), false},
		"a replaced stream": {cancelled, wa("351912345678", "x", now), false},
		"no send time":      {context.Background(), noTime, false},
		"future":            {context.Background(), wa("351912345678", "x", now.Add(2*time.Minute)), false},
		"a little ahead":    {context.Background(), wa("351912345678", "x", now.Add(30*time.Second)), true},
	} {
		o := &fakeOwners{got: make(chan ownerMsg, 1)}
		deliver(c.ctx, o, s, clock, "351912345678", c.m.GetWhatsapp())
		if got := len(o.got) == 1; got != c.ok {
			t.Errorf("%s: delivered = %v, want %v", name, got, c.ok)
		}
	}
}

func TestNoPhoneNoListener(t *testing.T) {
	r := newWARig(t, "", true)
	time.Sleep(200 * time.Millisecond)
	if n := r.fake.Listeners(); n != 0 {
		t.Fatalf("listeners = %d", n)
	}
}

// serveH2C serves h on a Unix socket, HTTP/2 only, as whatsappd does.
func serveH2C(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	sock := filepath.Join(sockDir(t), "wa.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, Protocols: h2c.Protocols()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestSendGoesToTheOwnersPhone(t *testing.T) {
	var mu sync.Mutex
	var body map[string]string
	socket := serveH2C(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/whatsapp/send" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		body = map[string]string{}
		json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusNoContent)
	})
	s := &phoneSettings{phone: "+351912345678"}
	ch := NewWhatsAppChannel(socket, s)
	if err := ch.Send(context.Background(), "done"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if body["chat"] != "+351912345678" || body["text"] != "done" {
		t.Fatalf("body = %v", body)
	}
	mu.Unlock()
	// The phone is read fresh for each send.
	s.set("+351944444444", true)
	if err := ch.Send(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if body["chat"] != "+351944444444" {
		t.Fatalf("body = %v", body)
	}
}

func TestSendWithNoPhoneSendsNothing(t *testing.T) {
	var mu sync.Mutex
	called := false
	socket := serveH2C(t, func(http.ResponseWriter, *http.Request) { mu.Lock(); called = true; mu.Unlock() })
	err := NewWhatsAppChannel(socket, &phoneSettings{}).Send(context.Background(), "x")
	mu.Lock()
	defer mu.Unlock()
	if err == nil || called {
		t.Fatalf("err = %v, called = %v", err, called)
	}
}
