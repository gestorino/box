// Package channels is the seam for the communication clients (email, whatsapp, sms, phone).
// Each client implements Channel; the engine only ever talks to the interface.
package channels

import (
	"context"
	"fmt"
	"slices"
)

// Message is one outbound message to a counterparty.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Channel is one communication client.
type Channel interface {
	Name() string
	Send(ctx context.Context, m Message) error
}

// Registry holds the channels the engine was started with.
type Registry struct{ byName map[string]Channel }

func NewRegistry(chs ...Channel) (*Registry, error) {
	r := &Registry{byName: map[string]Channel{}}
	for _, c := range chs {
		if _, dup := r.byName[c.Name()]; dup {
			return nil, fmt.Errorf("channels: %q registered twice", c.Name())
		}
		r.byName[c.Name()] = c
	}
	return r, nil
}

func (r *Registry) Get(name string) (Channel, bool) {
	c, ok := r.byName[name]
	return c, ok
}

// Names returns the registered channel names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
