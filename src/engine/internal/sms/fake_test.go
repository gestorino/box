package sms

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// fakeModem is ModemManager's message list: tests set what it holds and which instance is current.
type fakeModem struct {
	mu        sync.Mutex
	instance  string
	msgs      []ModemSMS
	deleted   []string
	deleteErr error
	onList    func()      // runs after List returns its snapshot (simulate a restart in between)
	sent      [][2]string // number, text
	sendErr   error
}

func (f *fakeModem) Send(_ context.Context, number, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, [2]string{number, text})
	// ModemManager keeps a sent message in its list, as "sent", until it is deleted.
	f.msgs = append(f.msgs, ModemSMS{Path: fmt.Sprintf("/SMS/sent%d", len(f.sent)), Number: number, Text: text, State: "sent", PDUType: "submit", Reference: uint32(len(f.sent))})
	return nil
}

func (f *fakeModem) List(context.Context) (string, []ModemSMS, error) {
	f.mu.Lock()
	inst, msgs := f.instance, append([]ModemSMS(nil), f.msgs...)
	hook := f.onList
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return inst, msgs, nil
}

func (f *fakeModem) Delete(_ context.Context, instance, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if instance != f.instance {
		return ErrModemChanged
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, path)
	kept := f.msgs[:0]
	for _, m := range f.msgs {
		if m.Path != path {
			kept = append(kept, m)
		}
	}
	f.msgs = kept
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
