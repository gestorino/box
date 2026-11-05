package listen

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/gen/listen/v1/listenv1connect"
	"concierge/engine/internal/h2c"
)

// Listener opens listeners on a connector.
type Listener interface {
	// Listen streams the events of a listener for req until ctx ends, when the channel closes.
	Listen(ctx context.Context, req *listenv1.ListenRequest) <-chan *listenv1.Event
}

// maxBackoff caps the wait between re-opens of a listener.
const maxBackoff = 30 * time.Second

// Client is the engine's side of a connector's listen service, over unencrypted HTTP/2 on the
// connector's Unix socket.
type Client struct {
	rpc     listenv1connect.ListenServiceClient
	backoff time.Duration // the first wait before a re-open; it doubles up to maxBackoff
}

var _ Listener = (*Client)(nil)

// NewClient returns a client of the connector listening on socket.
func NewClient(socket string) *Client {
	// No http.Client.Timeout: a listener stays open, idle, for as long as its ctx lives.
	return &Client{
		rpc:     listenv1connect.NewListenServiceClient(&http.Client{Transport: h2c.Transport(socket)}, "http://listen"),
		backoff: 200 * time.Millisecond,
	}
}

// Listen opens a listener for req and keeps it open until ctx ends: when the stream drops or the
// connector is unreachable it re-opens, backing off, replaying from req.Since or, for a live-only
// listener, from the moment it first opened, so nothing stored meanwhile is lost. It passes on
// each message once and only the first caught_up. A live-only listener guarantees nothing stored
// before its first caught_up. A filter the connector refuses as invalid is a bug, not a wait: the
// channel closes.
func (c *Client) Listen(ctx context.Context, req *listenv1.ListenRequest) <-chan *listenv1.Event {
	out := make(chan *listenv1.Event)
	go c.run(ctx, req, out)
	return out
}

func (c *Client) run(ctx context.Context, req *listenv1.ListenRequest, out chan<- *listenv1.Event) {
	defer close(out)

	reopen := proto.Clone(req).(*listenv1.ListenRequest)
	if reopen.Since == nil {
		reopen.Since = timestamppb.Now()
	}
	delivered := map[int64]bool{}
	caughtUp := false
	deliver := func(e *listenv1.Event) bool {
		switch {
		case e.GetCaughtUp() != nil:
			if caughtUp {
				return true
			}
			caughtUp = true
		case e.GetMessage() != nil:
			if delivered[e.GetMessage().GetId()] {
				return true
			}
			delivered[e.GetMessage().GetId()] = true
		}
		select {
		case out <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}

	wait := c.backoff
	for open := req; ; open = reopen {
		received, err := c.open(ctx, open, deliver)
		if ctx.Err() != nil {
			return
		}
		if connect.CodeOf(err) == connect.CodeInvalidArgument {
			slog.Warn("listen: the connector refused the listener's filter as invalid, not retrying")
			return
		}
		if received {
			wait = c.backoff
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		wait = min(2*wait, maxBackoff)
	}
}

// open runs one stream, handing each event to deliver until it returns false; it reports
// whether any event arrived and why the stream ended.
func (c *Client) open(ctx context.Context, req *listenv1.ListenRequest, deliver func(*listenv1.Event) bool) (bool, error) {
	stream, err := c.rpc.Listen(ctx, connect.NewRequest(req))
	if err != nil {
		return false, err
	}
	defer stream.Close()
	received := false
	for stream.Receive() {
		received = true
		if !deliver(stream.Msg()) {
			return received, ctx.Err()
		}
	}
	return received, stream.Err()
}
