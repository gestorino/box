package listen

import (
	"sync"

	listenv1 "concierge/engine/internal/gen/listen/v1"
)

// subBuffer is how many messages a listener may fall behind before the hub drops it.
const subBuffer = 64

// Hub fans each published message out to the subscriptions it matches.
type Hub struct {
	match Match

	mu   sync.Mutex
	subs map[*Sub]struct{}
}

// NewHub returns a hub that routes with match.
func NewHub(match Match) *Hub {
	return &Hub{match: match, subs: map[*Sub]struct{}{}}
}

// Sub is one open listener.
type Sub struct {
	hub     *Hub
	req     *listenv1.ListenRequest
	msgs    chan *listenv1.Message
	dropped chan struct{}
}

// Subscribe opens a listener for req.
func (h *Hub) Subscribe(req *listenv1.ListenRequest) *Sub {
	s := &Sub{
		hub:     h,
		req:     req,
		msgs:    make(chan *listenv1.Message, subBuffer),
		dropped: make(chan struct{}),
	}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

// Publish hands m to every matching sub and never blocks: a sub whose buffer is full is
// removed and its Dropped channel closed, so the listener reconnects and catches up.
func (h *Hub) Publish(m *listenv1.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if !h.match(s.req, m) {
			continue
		}
		select {
		case s.msgs <- m:
		default:
			delete(h.subs, s)
			close(s.dropped)
		}
	}
}

// Len is the number of open subscriptions.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Messages delivers the matching messages in the order they were published.
func (s *Sub) Messages() <-chan *listenv1.Message { return s.msgs }

// Dropped is closed when the hub dropped the sub because its buffer was full.
func (s *Sub) Dropped() <-chan struct{} { return s.dropped }

// Close ends the subscription; it is safe to call twice, or after a drop.
func (s *Sub) Close() {
	s.hub.mu.Lock()
	delete(s.hub.subs, s)
	s.hub.mu.Unlock()
}
