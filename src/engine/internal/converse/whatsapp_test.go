package converse

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "concierge/engine/internal/gen/agent/v1"
	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/h2c"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/listen/listentest"
	"concierge/engine/internal/signins"
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

func (f *fakeOwners) Owner(_ context.Context, channel, thread, text string, _ Source) {
	f.got <- ownerMsg{channel, thread, text}
}

func (f *fakeOwners) LastHeard(context.Context, string) (time.Time, bool, error) {
	return time.Time{}, false, nil
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

// waIDs numbers the test's WhatsApp messages, as WhatsApp gives each its own id.
var waIDs atomic.Int64

func wa(phone, text string, sent time.Time) *listenv1.Message {
	return &listenv1.Message{Body: &listenv1.Message_Whatsapp{Whatsapp: &listenv1.WhatsApp{
		Chat: phone + "@s.whatsapp.net", Id: "3EB0" + strconv.FormatInt(waIDs.Add(1), 10), Phone: phone, Text: text,
		SentAt: timestamppb.New(sent)}}}
}

// A WhatsApp message is known by its chat and WhatsApp's own id, never by whatsappd's row: rows are
// numbered again after its archive is wiped (WhatsApp removed with its data, then added back).
func TestAWhatsAppMessageIsKnownByItsOwnIDNotItsRow(t *testing.T) {
	at := time.Now()
	before := wa("351912345678", "old", at)
	before.Id, before.GetWhatsapp().Id = 5, "3EB0OLD"
	after := wa("351912345678", "new", at)
	after.Id, after.GetWhatsapp().Id = 5, "3EB0NEW" // the same row number, after a wipe
	a, b := sourceOf(before), sourceOf(after)
	if a.Channel != "whatsapp" || a.ID != "351912345678@s.whatsapp.net/3EB0OLD" || a.key() == b.key() {
		t.Fatalf("sources %+v and %+v", a, b)
	}
	if again := sourceOf(before); again.key() != a.key() {
		t.Fatalf("a replay of the same message is %q, not %q", again.key(), a.key())
	}
	noID := wa("351912345678", "?", at)
	noID.GetWhatsapp().Id = ""
	if got := sourceOf(noID); got.key() != "" {
		t.Fatalf("a message with no WhatsApp id is %q", got.key())
	}

	s, ctx := openStore(t), context.Background()
	conv := open(t, s, "chat")
	if _, err := s.Intake(ctx, conv, owner("old"), at, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Intake(ctx, conv, owner("new"), at, b); err != nil {
		t.Fatalf("the new message on a reused row: %v", err)
	}
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
		deliver(c.ctx, o, s, clock, "351912345678", c.m)
		if got := len(o.got) == 1; got != c.ok {
			t.Errorf("%s: delivered = %v, want %v", name, got, c.ok)
		}
	}
}

func TestMessagesWhileTheEngineWasDownReachTheHubOnceAfterItRestarts(t *testing.T) {
	fake := listentest.NewFake(listen.MatchWhatsApp)
	srv := listentest.Serve(t, fake)
	path := filepath.Join(t.TempDir(), "agent.db")
	switchOn := &phoneSettings{phone: "+351912345678", whatsapp: true}
	step := func(_ int, req *agentv1.StepRequest) (*agentv1.StepResponse, error) {
		last, _ := lastOwner(req)
		return say("re: " + last), nil
	}
	// engine runs a hub over agent.db and its WhatsApp listener, as the engine does, until stopped.
	engine := func() (*rig, func()) {
		store, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		r := newRig(t, store, step)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			listenWhatsApp(ctx, listen.NewClient(srv.Socket), r.hub, switchOn, time.Now, 20*time.Millisecond)
			close(done)
		}()
		return r, sync.OnceFunc(func() { cancel(); <-done; r.stop(); store.Close() })
	}

	fake.Add(wa("351912345678", "one", time.Now().Add(-2*time.Minute))) // the engine is not running yet
	first, stop := engine()
	if got := first.channel.next(t); got != "re: one" {
		t.Fatalf("sent %q", got)
	}
	first.settle(t)
	stop()

	fake.Add(wa("351912345678", "two", time.Now())) // the engine is down
	second, stop := engine()
	defer stop()
	if got := second.channel.next(t); got != "re: two" {
		t.Fatalf("after the restart sent %q", got)
	}
	second.settle(t)
	time.Sleep(100 * time.Millisecond) // a replay of "one" would be answered by now
	second.settle(t)
	second.channel.none(t)
	var said []string
	_, events := second.only(t)
	for _, e := range events {
		if o, ok := e.GetKind().(*agentv1.Event_Owner); ok {
			said = append(said, o.Owner)
		}
	}
	if !equal(said, []string{"one", "two"}) {
		t.Fatalf("the owner's messages kept: %q", said)
	}
}

func TestAListenerResumesWhereTheHubLeftOffButNoEarlierThanMaxAge(t *testing.T) {
	now := t0.Add(time.Hour)
	for name, c := range map[string]struct {
		last time.Time
		ok   bool
		want time.Time
	}{
		"none heard":     {time.Time{}, false, now.Add(-maxAge)},
		"heard lately":   {now.Add(-3 * time.Minute), true, now.Add(-3 * time.Minute)},
		"heard long ago": {now.Add(-time.Hour), true, now.Add(-maxAge)},
	} {
		if got := resumeAt(context.Background(), lastHeard{c.last, c.ok}, "whatsapp", now); !got.Equal(c.want) {
			t.Errorf("%s: since %v, want %v", name, got, c.want)
		}
	}
}

// lastHeard is owners whose newest message taken in is fixed.
type lastHeard struct {
	at time.Time
	ok bool
}

func (lastHeard) Owner(context.Context, string, string, string, Source) {}
func (l lastHeard) LastHeard(context.Context, string) (time.Time, bool, error) {
	return l.at, l.ok, nil
}

func TestASwitchThatCantBeReadStillKeepsTheMessage(t *testing.T) {
	now := time.Now()
	s := &fakeSettings{agent: "claude", whatsapp: true}
	s.fail(1)
	o := &fakeOwners{got: make(chan ownerMsg, 1)}
	deliver(context.Background(), o, s, func() time.Time { return now }, "351912345678", wa("351912345678", "x", now))
	if len(o.got) != 1 {
		t.Fatal("hostd away dropped the owner's message: the hub keeps it and decides once it can read the switch")
	}
	s.set("claude", false)
	deliver(context.Background(), o, s, func() time.Time { return now }, "351912345678", wa("351912345678", "y", now))
	if len(o.got) != 1 {
		t.Fatal("a message reached the hub with the switch off")
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

// sendServer is whatsappd's send that keeps each text it got, in order, and fails the nth (from 1;
// 0 fails none) as whatsappd refuses one.
func sendServer(t *testing.T, failing int) (socket string, got func() []string) {
	t.Helper()
	var mu sync.Mutex
	var texts []string
	socket = serveH2C(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		mu.Lock()
		defer mu.Unlock()
		texts = append(texts, body["text"])
		if len(texts) == failing {
			http.Error(w, "too long", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return socket, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), texts...)
	}
}

func TestALongTextIsSentInPartsInOrder(t *testing.T) {
	para := func(r string) string { return strings.Repeat(r, 2999) + "." }
	text := para("a") + "\n\n" + para("é") + "\n\n" + para("ç") // 9004 runes
	socket, got := sendServer(t, 0)
	if err := NewWhatsAppChannel(socket, &phoneSettings{phone: "+351912345678"}).Send(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	if parts := got(); !equal(parts, []string{para("a"), para("é"), para("ç")}) {
		t.Fatalf("%d parts of %v runes", len(parts), runeCounts(parts))
	}
	for name, c := range map[string]struct {
		text string
		want []int
	}{
		"lines":  {strings.Repeat(strings.Repeat("l", 99)+"\n", 90), []int{3999, 3999, 1000}},
		"words":  {strings.Repeat(strings.Repeat("w", 9)+" ", 900), []int{3999, 3999, 1000}},
		"no gap": {strings.Repeat("ü", 9000), []int{4000, 4000, 1000}},
		"short":  {"done", []int{4}},
	} {
		if n := runeCounts(sendParts(c.text)); !equal(intWords(n), intWords(c.want)) {
			t.Errorf("%s: parts of %v runes, want %v", name, n, c.want)
		}
		if joined := strings.Join(sendParts(c.text), ""); utf8.RuneCountInString(joined) > utf8.RuneCountInString(c.text) {
			t.Errorf("%s: the parts grew", name)
		}
	}
}

func TestAFailedPartFailsTheSend(t *testing.T) {
	socket, got := sendServer(t, 2)
	text := strings.Repeat("x", 3000) + "\n\n" + strings.Repeat("y", 3000) + "\n\n" + strings.Repeat("z", 3000)
	if err := NewWhatsAppChannel(socket, &phoneSettings{phone: "+351912345678"}).Send(context.Background(), text); err == nil {
		t.Fatal("a failed part was not an error")
	}
	if n := len(got()); n != 2 {
		t.Fatalf("%d parts sent after the second failed", n)
	}
}

func TestALongConfirmWithAFailedPartIsNotTold(t *testing.T) {
	r := newRig(t, openStore(t), answeringAgent)
	socket, got := sendServer(t, 2)
	r.hub.cfg.Channels["whatsapp"] = NewWhatsAppChannel(socket, r.settings)
	conv := r.linked(t, 41)
	r.errands.wait(41, "confirm", 9)
	r.hub.RunEvent(signins.RunEvent{Run: 41, Kind: "confirm", Text: "Confirm: " + strings.Repeat("all of it ", 900) + "?",
		Options: []string{"Yes", "No"}, Wait: 9})
	r.settle(t)
	if n := len(got()); n != 2 {
		t.Fatalf("%d parts sent", n)
	}
	if _, _, _, ok, err := r.store.pendingConfirm(context.Background(), conv, 41); ok || err != nil {
		t.Fatalf("a Confirm half sent counts as told: %v %v", ok, err)
	}
}

func runeCounts(parts []string) []int {
	out := make([]int, len(parts))
	for i, p := range parts {
		out[i] = utf8.RuneCountInString(p)
	}
	return out
}

func intWords(ns []int) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = strconv.Itoa(n)
	}
	return out
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
