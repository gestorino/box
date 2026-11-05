package calls

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"
)

const purgeEvery = time.Hour

type live struct {
	id       int64
	answered bool
	lastSeen time.Time
}

// Archiver follows ModemManager's calls tick by tick (2 s in calld): a new call is recorded as it
// starts, answered when it first goes active, ended with its outcome when terminated — then cleared
// from ModemManager so a restart never records it again.
type Archiver struct {
	m         Modem
	a         *Archive
	retention func() (days int, known bool)
	debugDays func() (days int, known bool)
	now       func() time.Time

	live           map[string]*live    // instance|path
	done           map[string]struct{} // recorded, not yet cleared from ModemManager
	lastPurge      time.Time
	lastDays       int
	lastDebugPurge time.Time
	lastDebugDays  int

	heldMu sync.Mutex
	held   map[string]bool  // paths the Caller is still reading: recorded, but not cleared yet
	heldID map[string]int64 // the row of each held path, once recorded
	kept   map[string]Kept  // what the Caller kept on a held path not recorded yet
}

// Hold keeps the call at path in ModemManager after it ends — recorded as usual, cleared only once
// released — so the Caller that placed it can still read how it ended.
func (r *Archiver) Hold(path string) (release func()) {
	r.heldMu.Lock()
	r.held[path] = true
	r.heldMu.Unlock()
	return func() {
		r.heldMu.Lock()
		delete(r.held, path)
		delete(r.heldID, path)
		if k, ok := r.kept[path]; ok { // never recorded: no row will take it
			discard(k)
			delete(r.kept, path)
		}
		r.heldMu.Unlock()
	}
}

// Keep puts what the Caller kept on the held call at path on that call's row — now, or as soon as
// the archiver records it. Kept for a call released before it was recorded is dropped, its
// recording deleted.
func (r *Archiver) Keep(path string, k Kept) {
	r.heldMu.Lock()
	defer r.heldMu.Unlock()
	if id, ok := r.heldID[path]; ok {
		r.store(id, k)
		return
	}
	if !r.held[path] {
		discard(k)
		return
	}
	r.kept[path] = k
}

// recorded links a held path to its row and stores what was kept before. Called under no lock.
func (r *Archiver) recorded(path string, id int64) {
	r.heldMu.Lock()
	defer r.heldMu.Unlock()
	if !r.held[path] {
		return
	}
	r.heldID[path] = id
	if k, ok := r.kept[path]; ok {
		r.store(id, k)
		delete(r.kept, path)
	}
}

// store writes k on row id; errors are logged, never the words. The recording moves from the
// Caller's tmp to the archive's audio directory: one rename on the same filesystem, so it is either
// there whole or not at all.
func (r *Archiver) store(id int64, k Kept) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if k.Summary != "" {
		if err := r.a.SetSummary(ctx, id, k.Summary); err != nil {
			slog.Error("calls: keeping the call's summary", "err", err)
		}
	}
	if k.Result != "" || k.Prompt != "" {
		if err := r.a.SetResult(ctx, id, k.Result, k.Prompt); err != nil {
			slog.Error("calls: keeping the call's result", "err", err)
		}
	}
	if k.Usage != nil {
		if err := r.a.SetUsage(ctx, id, k.Usage); err != nil {
			slog.Error("calls: keeping the call's usage", "err", err)
		}
	}
	if len(k.Turns) > 0 {
		if err := r.a.AddTurns(ctx, id, k.Turns); err != nil {
			slog.Error("calls: keeping the call's transcript", "err", err)
		}
	}
	if k.Audio != "" {
		if err := os.Rename(k.Audio, r.a.AudioPath(id)); err != nil {
			slog.Error("calls: keeping the call's recording", "err", err)
			os.Remove(k.Audio)
		}
	}
}

// discard deletes the recording of a Kept that no row will take.
func discard(k Kept) {
	if k.Audio != "" {
		if err := os.Remove(k.Audio); err != nil && !os.IsNotExist(err) {
			slog.Error("calls: deleting an unkept recording", "err", err)
		}
	}
}

// NewArchiver: retention is the call log's, debugDays the transcripts' and recordings'; known is
// false for either until hostd first answers.
func NewArchiver(m Modem, a *Archive, retention, debugDays func() (days int, known bool), now func() time.Time) *Archiver {
	return &Archiver{m: m, a: a, retention: retention, debugDays: debugDays, now: now, live: map[string]*live{}, done: map[string]struct{}{},
		lastDays: -1, lastDebugDays: -1, held: map[string]bool{}, heldID: map[string]int64{}, kept: map[string]Kept{}}
}

func (r *Archiver) Tick(ctx context.Context) error {
	instance, calls, err := r.m.List(ctx)
	if err != nil {
		return err
	}
	now := r.now()
	seen := map[string]bool{}
	for _, c := range calls {
		key := instance + "|" + c.Path
		seen[key] = true
		if _, ok := r.done[key]; ok {
			r.clear(ctx, instance, c.Path, key)
			continue
		}
		l, tracked := r.live[key]
		if !tracked {
			if c.State == "terminated" { // finished before calld saw it: no times to trust
				id, err := r.a.Start(ctx, c.Number, c.Direction, now)
				if err == nil {
					r.recorded(c.Path, id)
					err = r.a.End(ctx, id, now, "unknown")
				}
				if err != nil {
					slog.Error("calls: recording a finished call", "path", c.Path, "err", err)
					continue
				}
				r.done[key] = struct{}{}
				r.clear(ctx, instance, c.Path, key)
				continue
			}
			id, err := r.a.Start(ctx, c.Number, c.Direction, now)
			if err != nil {
				slog.Error("calls: recording a call", "path", c.Path, "err", err)
				continue
			}
			r.recorded(c.Path, id)
			l = &live{id: id}
			r.live[key] = l
		}
		l.lastSeen = now
		if c.State == "active" && !l.answered {
			if err := r.a.MarkAnswered(ctx, l.id, now); err == nil {
				l.answered = true
			}
		}
		if c.State == "terminated" {
			if err := r.a.End(ctx, l.id, now, outcome(c.Direction, l.answered, c.Reason)); err != nil {
				slog.Error("calls: ending a call", "path", c.Path, "err", err)
				continue
			}
			delete(r.live, key)
			r.done[key] = struct{}{}
			r.clear(ctx, instance, c.Path, key)
		}
	}
	for key, l := range r.live { // gone without terminating: ModemManager restarted mid-call
		if !seen[key] {
			if err := r.a.End(ctx, l.id, l.lastSeen, "unknown"); err == nil {
				delete(r.live, key)
			}
		}
	}
	for key := range r.done {
		if !seen[key] {
			delete(r.done, key)
		}
	}
	r.purge(ctx)
	return nil
}

func (r *Archiver) clear(ctx context.Context, instance, path, key string) {
	r.heldMu.Lock()
	held := r.held[path]
	r.heldMu.Unlock()
	if held { // stays in done: cleared on a tick after its release
		return
	}
	err := r.m.Delete(ctx, instance, path)
	switch {
	case err == nil:
		delete(r.done, key)
	case errors.Is(err, ErrModemChanged):
	default:
		slog.Warn("calls: clearing from ModemManager, will retry", "path", path, "err", err)
	}
}

// purge runs the call log's retention, then the debug material's, each hourly or when its days
// change. Until hostd has answered neither runs: waiting loses nothing, guessing could.
func (r *Archiver) purge(ctx context.Context) {
	if days, known := r.retention(); known && (days != r.lastDays || r.now().Sub(r.lastPurge) >= purgeEvery) {
		if err := r.a.Purge(ctx, days); err != nil {
			slog.Error("calls: purge", "err", err)
		} else {
			r.lastDays, r.lastPurge = days, r.now()
		}
	}
	if days, known := r.debugDays(); known && (days != r.lastDebugDays || r.now().Sub(r.lastDebugPurge) >= purgeEvery) {
		if err := r.a.PurgeDebug(ctx, days); err != nil {
			slog.Error("calls: purging transcripts and recordings", "err", err)
		} else {
			r.lastDebugDays, r.lastDebugPurge = days, r.now()
		}
	}
}

// outcome is how a finished call ended, in a person's words.
func outcome(direction string, answered bool, reason string) string {
	if answered {
		return "answered"
	}
	if direction == "incoming" {
		if reason == "refused-or-busy" {
			return "declined"
		}
		return "missed"
	}
	switch reason {
	case "refused-or-busy":
		return "busy"
	case "error", "audio-setup-failed":
		return "failed"
	}
	return "no-answer"
}

func (r *Archiver) Run(ctx context.Context, every time.Duration) {
	if err := r.a.CloseLive(ctx, r.now()); err != nil {
		slog.Error("calls: closing calls left open", "err", err)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := r.Tick(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("calls: modem unavailable", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
