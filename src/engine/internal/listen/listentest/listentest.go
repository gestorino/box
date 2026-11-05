// Package listentest serves a listen service on a Unix socket for tests, and fakes a connector's.
package listentest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/gen/listen/v1/listenv1connect"
	"concierge/engine/internal/h2c"
	"concierge/engine/internal/listen"
)

// Server is a listen service on a Unix socket, as a daemon serves it.
type Server struct {
	Socket string

	mu     sync.Mutex
	protos []string
}

// Serve serves h on a Unix socket under a short temp dir (macOS caps socket paths at 104 bytes)
// with a daemon's protocols; it closes on cleanup.
func Serve(t testing.TB, h listenv1connect.ListenServiceHandler) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "listen")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &Server{Socket: filepath.Join(dir, "s.sock")}
	ln, err := net.Listen("unix", s.Socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(listenv1connect.NewListenServiceHandler(h))
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			s.protos = append(s.protos, r.Proto)
			s.mu.Unlock()
			mux.ServeHTTP(w, r)
		}),
		Protocols:         h2c.Protocols(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return s
}

// Protocols is each request's protocol, in the order they came.
func (s *Server) Protocols() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.protos...)
}

var errKicked = errors.New("kicked by the fake")

// Fake is a connector's listen service over an in-memory archive.
type Fake struct {
	match listen.Match
	hub   *listen.Hub

	mu       sync.Mutex
	msgs     []*listenv1.Message
	fail     connect.Code
	onReplay func()
	replays  int
	streams  map[int]context.CancelCauseFunc
	nextID   int64
	nextOpen int
}

var _ listenv1connect.ListenServiceHandler = (*Fake)(nil)

// NewFake returns a fake connector that routes with match.
func NewFake(match listen.Match) *Fake {
	return &Fake{match: match, hub: listen.NewHub(match), streams: map[int]context.CancelCauseFunc{}}
}

// Add stores m with the next Id and StoredAt now, then hands it to the open listeners.
func (f *Fake) Add(m *listenv1.Message) {
	f.mu.Lock()
	f.nextID++
	m.Id = f.nextID
	m.StoredAt = timestamppb.Now()
	f.msgs = append(f.msgs, m)
	f.mu.Unlock()
	f.hub.Publish(m)
}

// Fail refuses every open with code until Fail(0).
func (f *Fake) Fail(code connect.Code) {
	f.mu.Lock()
	f.fail = code
	f.mu.Unlock()
}

// Kick ends every open stream with Unavailable.
func (f *Fake) Kick() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cancel := range f.streams {
		cancel(errKicked)
	}
}

// OnReplay runs fn inside the next replay: after the listener subscribed, before the archive is
// read.
func (f *Fake) OnReplay(fn func()) {
	f.mu.Lock()
	f.onReplay = fn
	f.mu.Unlock()
}

// Replays is the number of replays that have read the archive: once a listener's has, whatever is
// stored next reaches it live, after its caught_up.
func (f *Fake) Replays() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replays
}

// Listeners is the number of open subscriptions.
func (f *Fake) Listeners() int { return f.hub.Len() }

// Listen implements listenv1connect.ListenServiceHandler.
func (f *Fake) Listen(ctx context.Context, req *connect.Request[listenv1.ListenRequest], stream *connect.ServerStream[listenv1.Event]) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	f.mu.Lock()
	if f.fail != 0 {
		code := f.fail
		f.mu.Unlock()
		return connect.NewError(code, errors.New("refused by the fake"))
	}
	f.nextOpen++
	open := f.nextOpen
	f.streams[open] = cancel
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.streams, open)
		f.mu.Unlock()
	}()

	err := listen.Stream(ctx, f.hub, req.Msg, f.replay, stream.Send)
	if errors.Is(context.Cause(ctx), errKicked) {
		return connect.NewError(connect.CodeUnavailable, errKicked)
	}
	return err
}

func (f *Fake) replay(_ context.Context, req *listenv1.ListenRequest) ([]*listenv1.Message, error) {
	f.mu.Lock()
	fn := f.onReplay
	f.onReplay = nil
	f.mu.Unlock()
	if fn != nil {
		fn()
	}

	since := req.GetSince().AsTime()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replays++
	var out []*listenv1.Message
	for _, m := range f.msgs {
		if !m.GetStoredAt().AsTime().Before(since) && f.match(req, m) {
			out = append(out, m)
		}
	}
	return out, nil
}
