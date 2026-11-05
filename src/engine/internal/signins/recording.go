package signins

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"concierge/engine/internal/browser"
)

// RunPart is one finished stretch of a run's video: one browser request of the run (its flow, its
// check). StartedAt is when the file began, DurationMS how long it ran.
type RunPart struct {
	N          int       `json:"n"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
}

// RunClick is a click that went through, and where on the screen: a mark on the video.
type RunClick struct {
	At time.Time `json:"at"`
	X  int       `json:"x"`
	Y  int       `json:"y"`
}

// partRow is a part as the store holds it: ended is zero until its request returned.
type partRow struct {
	n       int
	started time.Time
	ended   time.Time
}

func (s *Store) nextPart(ctx context.Context, run int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(n), 0) + 1 FROM run_parts WHERE run = ?`, run).Scan(&n)
	return n, err
}

func (s *Store) StartPart(ctx context.Context, run int64, n int, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_parts (run, n, started_at, ended_at) VALUES (?, ?, ?, '')
		ON CONFLICT (run, n) DO UPDATE SET started_at = excluded.started_at, ended_at = ''`, run, n, ts(at))
	return err
}

// EndParts ends the run's part still being written, if there is one.
func (s *Store) EndParts(ctx context.Context, run int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE run_parts SET ended_at = ? WHERE run = ? AND ended_at = ''`, ts(at), run)
	return err
}

func (s *Store) AddClick(ctx context.Context, run int64, at time.Time, x, y int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_clicks (run, at, x, y) VALUES (?, ?, ?, ?)`, run, ts(at), x, y)
	return err
}

func (s *Store) partRows(ctx context.Context, run int64) ([]partRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT n, started_at, ended_at FROM run_parts WHERE run = ? ORDER BY n`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []partRow
	for rows.Next() {
		var p partRow
		var started, ended string
		if err := rows.Scan(&p.n, &started, &ended); err != nil {
			return nil, err
		}
		if p.started, err = parseTS(started); err != nil {
			return nil, err
		}
		if p.ended, err = parseTS(ended); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Clicks are the run's marks in the order they came.
func (s *Store) Clicks(ctx context.Context, run int64) ([]RunClick, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, x, y FROM run_clicks WHERE run = ? ORDER BY rowid`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunClick{}
	for rows.Next() {
		var c RunClick
		var at string
		if err := rows.Scan(&at, &c.X, &c.Y); err != nil {
			return nil, err
		}
		if c.At, err = parseTS(at); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// recordingsOn is whether this box records its runs: it has screens, and somewhere to keep them.
func (c Config) recordingsOn() bool { return c.ScreensDir != "" && c.RecordingsDir != "" }

// runDir is a run's directory under the recordings root. The worker writes it and the engine only
// reads it; the layout is the one convention the two share (the worker's concierge_worker/files.py):
//
//	<recordings>/<site>/<run>/<n>.mp4       part n (<n>.part.mp4 while it is written)
//	<recordings>/<site>/<run>/browser-use.log
//	<screens>/<site>/web.sock               the run's live view (screen.go)
func runDir(root, site string, run int64) string {
	return filepath.Join(root, site, strconv.FormatInt(run, 10))
}

// record is what a browser request of this run adds when the box records it: the run and the part
// it numbers, from which the worker makes the part's file and keeps the run within its budget.
// Nothing when the box does not record, or the part can't be numbered: a recording never fails a run.
func (l *runLog) record() browser.Message {
	if !l.recorded {
		return nil
	}
	n, err := l.store.nextPart(context.Background(), l.id)
	if err != nil {
		slog.Warn("signins: numbering a run's recording", "site", l.site, "run", l.id, "err", err)
		return nil
	}
	return browser.Message{"run": l.id, "part": n}
}

// partStarted is the worker saying its ffmpeg is up: the part's video begins now.
func (l *runLog) partStarted() {
	ctx := context.Background()
	n, err := l.store.nextPart(ctx, l.id)
	if err == nil {
		err = l.store.StartPart(ctx, l.id, n, l.now())
	}
	if err != nil {
		slog.Warn("signins: writing a run's part", "site", l.site, "run", l.id, "err", err)
	}
}

// partEnded is the request returning, error or not: the video of its part ends now.
func (l *runLog) partEnded() {
	if err := l.store.EndParts(context.Background(), l.id, l.now()); err != nil {
		slog.Warn("signins: ending a run's part", "site", l.site, "run", l.id, "err", err)
	}
}

func (l *runLog) clicked(x, y int) {
	if err := l.store.AddClick(context.Background(), l.id, l.now(), x, y); err != nil {
		slog.Warn("signins: writing a run's click", "site", l.site, "run", l.id, "err", err)
	}
}

// partFile is the file of part n: the remuxed one, else the part the worker was left with.
func (c Config) partFile(site string, run int64, n int) (string, bool) {
	for _, name := range []string{fmt.Sprintf("%d.mp4", n), fmt.Sprintf("%d.part.mp4", n)} {
		p := filepath.Join(runDir(c.RecordingsDir, site, run), name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return p, true
		}
	}
	return "", false
}

// Parts are the run's finished stretches of video that exist and hold something. A part whose
// request never returned plays up to the run's end or, if earlier (an engine that crashed and came
// back later), its file's last change; a run still going leaves it out.
func (s *Service) Parts(ctx context.Context, r Run) ([]RunPart, error) {
	out := []RunPart{}
	if !s.cfg.recordingsOn() {
		return out, nil
	}
	rows, err := s.cfg.Store.partRows(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	for _, p := range rows {
		end := p.ended
		if end.IsZero() && r.EndedAt == nil {
			continue
		}
		path, ok := s.cfg.partFile(r.Site, r.ID, p.n)
		if !ok {
			continue
		}
		if end.IsZero() {
			end = *r.EndedAt
			if info, err := os.Stat(path); err == nil && info.ModTime().Before(end) {
				end = info.ModTime()
			}
		}
		out = append(out, RunPart{N: p.n, StartedAt: p.started, DurationMS: max(end.Sub(p.started).Milliseconds(), 0)})
	}
	return out, nil
}

// pruneRecordings asks the worker to forget the site's recorded runs the store no longer keeps: the
// engine reads the site's run directories, which it may, and names those runs; the worker deletes
// them. Best effort: what fails is logged, and the next prune names the runs again.
func (s *Service) pruneRecordings(ctx context.Context, site string) {
	if s.cfg.RecordingsDir == "" || !isKey(site) {
		return
	}
	entries, err := os.ReadDir(filepath.Join(s.cfg.RecordingsDir, site))
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("signins: listing a site's recordings", "site", site, "err", err)
		}
		return
	}
	runs, err := s.cfg.Store.Runs(ctx, site)
	if err != nil {
		slog.Warn("signins: pruning recordings", "site", site, "err", err)
		return
	}
	kept := map[int64]bool{}
	for _, r := range runs {
		kept[r.ID] = true
	}
	var dropped []int64
	for _, e := range entries {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || id < 1 || strconv.FormatInt(id, 10) != e.Name() || !e.IsDir() || kept[id] {
			continue
		}
		dropped = append(dropped, id)
	}
	if len(dropped) == 0 {
		return
	}
	if _, err := s.cfg.Worker.Request(ctx, "forget_recordings", browser.Message{"site": site, "runs": dropped}); err != nil {
		slog.Warn("signins: forgetting dropped runs' recordings", "site", site, "runs", dropped, "err", err)
	}
}

// recording serves part n of a run with ranges, so the player can seek.
func (h *handler) recording(w http.ResponseWriter, r *http.Request) {
	site := r.PathValue("site")
	id, idErr := strconv.ParseInt(r.PathValue("id"), 10, 64)
	n, nErr := strconv.Atoi(r.PathValue("n"))
	notFound := func() { reply(w, http.StatusNotFound, map[string]string{"error": "no such recording"}) }
	if idErr != nil || nErr != nil || n < 1 || !isKey(site) {
		notFound()
		return
	}
	run, _, found, err := h.svc.cfg.Store.Run(r.Context(), site, id)
	if err != nil {
		fail(w, "recording", err)
		return
	}
	if !found {
		notFound()
		return
	}
	parts, err := h.svc.Parts(r.Context(), run)
	if err != nil {
		fail(w, "recording", err)
		return
	}
	var path string
	if slices.ContainsFunc(parts, func(p RunPart) bool { return p.N == n }) {
		path, _ = h.svc.cfg.partFile(site, id, n)
	}
	if path == "" {
		notFound()
		return
	}
	f, err := os.Open(path)
	if err != nil {
		notFound()
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		notFound()
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	http.ServeContent(w, r, "", info.ModTime(), f)
}
