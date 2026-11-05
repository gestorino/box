package sysbus

import (
	"context"
	"fmt"

	sd "github.com/coreos/go-systemd/v22/dbus"

	"concierge/engine/internal/hostd"
)

type units struct{ conn *sd.Conn }

// NewUnits starts and stops units through systemd's D-Bus API. polkit lets hostd's user do that for
// the connector units only (ansible/templates/50-concierge-host.rules.j2).
func NewUnits(ctx context.Context) (hostd.Units, error) {
	conn, err := sd.NewSystemConnectionContext(ctx)
	if err != nil {
		return nil, err
	}
	return &units{conn: conn}, nil
}

func (u *units) Start(ctx context.Context, unit string) error {
	return wait(ctx, unit, "start", func(ch chan<- string) (int, error) { return u.conn.StartUnitContext(ctx, unit, "replace", ch) })
}

func (u *units) Stop(ctx context.Context, unit string) error {
	return wait(ctx, unit, "stop", func(ch chan<- string) (int, error) { return u.conn.StopUnitContext(ctx, unit, "replace", ch) })
}

// wait queues the job and waits for its result; anything but "done" is an error.
func wait(ctx context.Context, unit, verb string, job func(chan<- string) (int, error)) error {
	ch := make(chan string, 1)
	if _, err := job(ch); err != nil {
		return fmt.Errorf("%s %s: %w", verb, unit, err)
	}
	select {
	case res := <-ch:
		if res != "done" {
			return fmt.Errorf("%s %s: job %s", verb, unit, res)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
