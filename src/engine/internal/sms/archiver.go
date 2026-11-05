package sms

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// receivingGrace is how long a multipart message may stay incomplete on the modem before what
// arrived is stored and the parts are cleared.
const receivingGrace = 24 * time.Hour

const purgeEvery = time.Hour

type Archiver struct {
	m         Modem
	a         *Archive
	retention func() (days int, known bool)
	now       func() time.Time

	mu        sync.Mutex           // one Tick at a time: Run's, or a send's
	firstSeen map[string]time.Time // receiving messages, by instance|path
	lastPurge time.Time
	lastDays  int
}

func NewArchiver(m Modem, a *Archive, retention func() (days int, known bool), now func() time.Time) *Archiver {
	return &Archiver{m: m, a: a, retention: retention, now: now, firstSeen: map[string]time.Time{}, lastDays: -1}
}

// Tick stores every finished message on the modem and clears it — store, confirm, then delete — and
// purges hourly or as soon as the retention changes. Errors leave messages on the modem for the next
// tick; message text is never logged.
func (r *Archiver) Tick(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	instance, msgs, err := r.m.List(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		key := instance + "|" + m.Path
		incomplete := false
		switch m.State {
		case "received", "sent":
		case "receiving":
			seen[key] = true
			first, ok := r.firstSeen[key]
			if !ok {
				r.firstSeen[key] = r.now()
				continue
			}
			if r.now().Sub(first) < receivingGrace || m.Text == "" && len(m.Data) == 0 {
				continue // nothing readable arrived: the parts stay on the modem
			}
			incomplete = true
		default: // stored drafts, sending: not finished
			continue
		}
		if err := r.archive(ctx, instance, m, incomplete); errors.Is(err, ErrModemChanged) {
			break // its paths now name other messages; the next tick lists afresh
		} else if err != nil {
			slog.Warn("sms: kept on the modem for the next pass", "path", m.Path, "err", err)
		}
	}
	for key := range r.firstSeen {
		if !seen[key] {
			delete(r.firstSeen, key)
		}
	}
	r.purge(ctx)
	return nil
}

func (r *Archiver) archive(ctx context.Context, instance string, m ModemSMS, incomplete bool) error {
	dedupe, err := r.a.Save(ctx, m, incomplete)
	if err != nil {
		return err
	}
	if ok, err := r.a.Has(ctx, dedupe); err != nil || !ok {
		return errors.Join(errors.New("stored row not confirmed"), err)
	}
	return r.m.Delete(ctx, instance, m.Path)
}

func (r *Archiver) purge(ctx context.Context) {
	days, known := r.retention()
	if !known { // hostd has not answered since start: waiting loses nothing, guessing could
		return
	}
	if days == r.lastDays && r.now().Sub(r.lastPurge) < purgeEvery {
		return
	}
	if err := r.a.Purge(ctx, days); err != nil {
		slog.Error("sms: purge", "err", err)
		return
	}
	r.lastDays, r.lastPurge = days, r.now()
}

// Run ticks every `every` until ctx ends.
func (r *Archiver) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := r.Tick(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("sms: modem unavailable", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
