package signins

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"concierge/engine/internal/browser"
)

// keptRuns is how many runs of a site the store keeps; starting one drops the older.
const keptRuns = 30

// Run is one background operation on a site: a registration or a check (which may sign in again),
// from start to end. State is running until it ends signed_in or warn (Reason is the account's), or
// ended when nothing was learnt (the worker went away, the engine stopped).
type Run struct {
	ID        int64      `json:"id"`
	Site      string     `json:"site"`
	Kind      string     `json:"kind"`
	State     string     `json:"state"`
	Reason    string     `json:"reason"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	// LLM is what the run spent on the model so far; nil for a run from before this was counted.
	LLM *RunLLM `json:"llm,omitempty"`
}

// RunLLM is a run's model use, every browser step of it added up. CostUSD is priced when each step
// reports, so a later price change doesn't rewrite it. Priced is false when some call has no known
// price (CostUSD then covers only the rest); Partial when a step died before it could report.
type RunLLM struct {
	Calls            int     `json:"calls"`
	Model            string  `json:"model"`
	InputTokens      int     `json:"input_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	Priced           bool    `json:"priced"`
	Partial          bool    `json:"partial"`
}

// add counts one step's reply: its `cost`, as the worker's `_cost` reports it.
func (u *RunLLM) add(cost map[string]any) {
	number := func(key string) int { n, _ := cost[key].(float64); return int(n) }
	calls := number("llm_calls")
	u.Calls += calls
	u.InputTokens += number("input_tokens")
	u.CachedTokens += number("cached_input_tokens")
	u.CacheWriteTokens += number("cache_write_tokens")
	u.OutputTokens += number("output_tokens")
	if calls == 0 {
		return
	}
	model, _ := cost["model"].(string)
	if model != "" && !slices.Contains(strings.Split(u.Model, ", "), model) {
		u.Model = strings.TrimPrefix(u.Model+", "+model, ", ")
	}
	// Every call goes through OpenRouter, which bills each one: its figure is the price.
	billed, ok := cost["billed_usd"].(float64)
	u.CostUSD += billed
	u.Priced = u.Priced && ok
}

// RunLine is one line of a run's log: box (what the engine did and learnt), goal (what the model set
// out to do on a browser step), action (a browser action and how it ended) or answer (a model answer nothing could read, as the
// model wrote it, scrubbed). Never a value.
type RunLine struct {
	ID   int64     `json:"id"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
}

// StartRun opens a running run and drops the site's runs past the newest keptRuns, with their lines.
func (s *Store) StartRun(ctx context.Context, site, kind string, at time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO runs (site, kind, state, reason, started_at, ended_at)
		VALUES (?, ?, 'running', '', ?, '')`, site, kind, ts(at))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_usage (run, calls, model, input_tokens, cached_tokens,
		cache_write_tokens, output_tokens, cost_usd, priced, partial) VALUES (?, 0, '', 0, 0, 0, 0, 0, 1, 0)`, id); err != nil {
		return 0, err
	}
	const old = `SELECT id FROM runs WHERE site = ? ORDER BY id DESC LIMIT -1 OFFSET ?`
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_parts WHERE run IN (`+old+`)`, site, keptRuns); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_clicks WHERE run IN (`+old+`)`, site, keptRuns); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_lines WHERE run IN (`+old+`)`, site, keptRuns); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_usage WHERE run IN (`+old+`)`, site, keptRuns); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_runs WHERE run IN (`+old+`)`, site, keptRuns); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id IN (`+old+`)`, site, keptRuns); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) AddLine(ctx context.Context, run int64, at time.Time, kind, text string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_lines (run, at, kind, text) VALUES (?, ?, ?, ?)`,
		run, ts(at), kind, text)
	return err
}

func (s *Store) EndRun(ctx context.Context, run int64, at time.Time, state, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET state = ?, reason = ?, ended_at = ? WHERE id = ?`,
		state, reason, ts(at), run)
	return err
}

// SetUsage writes the run's model use so far.
func (s *Store) SetUsage(ctx context.Context, run int64, u RunLLM) error {
	_, err := s.db.ExecContext(ctx, `UPDATE run_usage SET calls = ?, model = ?, input_tokens = ?, cached_tokens = ?,
		cache_write_tokens = ?, output_tokens = ?, cost_usd = ?, priced = ?, partial = ? WHERE run = ?`,
		u.Calls, u.Model, u.InputTokens, u.CachedTokens, u.CacheWriteTokens, u.OutputTokens, u.CostUSD, u.Priced, u.Partial, run)
	return err
}

const runColumns = `r.id, r.site, r.kind, r.state, r.reason, r.started_at, r.ended_at, u.run IS NOT NULL,
	COALESCE(u.calls, 0), COALESCE(u.model, ''), COALESCE(u.input_tokens, 0), COALESCE(u.cached_tokens, 0),
	COALESCE(u.cache_write_tokens, 0), COALESCE(u.output_tokens, 0), COALESCE(u.cost_usd, 0),
	COALESCE(u.priced, 0), COALESCE(u.partial, 0)`

const runTables = ` FROM runs r LEFT JOIN run_usage u ON u.run = r.id`

func scanRun(scan func(...any) error) (Run, error) {
	var r Run
	var started, ended string
	var counted bool
	var u RunLLM
	if err := scan(&r.ID, &r.Site, &r.Kind, &r.State, &r.Reason, &started, &ended, &counted,
		&u.Calls, &u.Model, &u.InputTokens, &u.CachedTokens, &u.CacheWriteTokens, &u.OutputTokens, &u.CostUSD, &u.Priced, &u.Partial); err != nil {
		return Run{}, err
	}
	if counted {
		r.LLM = &u
	}
	var err error
	if r.StartedAt, err = parseTS(started); err != nil {
		return Run{}, err
	}
	if ended != "" {
		e, err := parseTS(ended)
		if err != nil {
			return Run{}, err
		}
		r.EndedAt = &e
	}
	return r, nil
}

// Runs is the site's kept runs, newest first.
func (s *Store) Runs(ctx context.Context, site string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+runTables+` WHERE r.site = ? ORDER BY r.id DESC`, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Running is every run still running, oldest first.
func (s *Store) Running(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+runTables+` WHERE r.state = 'running' ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunsByID is the existing runs among ids; an id of a pruned run is absent.
func (s *Store) RunsByID(ctx context.Context, ids []int64) (map[int64]Run, error) {
	out := map[int64]Run{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+runTables+` WHERE r.id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		out[r.ID] = r
	}
	return out, rows.Err()
}

// Run is one run of the site with its lines in order; found is false for an id of another site.
func (s *Store) Run(ctx context.Context, site string, id int64) (Run, []RunLine, bool, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+runTables+` WHERE r.id = ? AND r.site = ?`, id, site).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, nil, false, nil
	}
	if err != nil {
		return Run{}, nil, false, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, kind, text FROM run_lines WHERE run = ? ORDER BY id`, id)
	if err != nil {
		return Run{}, nil, false, err
	}
	defer rows.Close()
	lines := []RunLine{}
	for rows.Next() {
		var l RunLine
		var at string
		if err := rows.Scan(&l.ID, &at, &l.Kind, &l.Text); err != nil {
			return Run{}, nil, false, err
		}
		if l.At, err = parseTS(at); err != nil {
			return Run{}, nil, false, err
		}
		lines = append(lines, l)
	}
	return r, lines, true, rows.Err()
}

// runLog writes one run's lines as they come. A write that fails is logged and the run goes on: the
// log is the owner's window on the run, never a reason for it to fail.
type runLog struct {
	store *Store
	now   func() time.Time
	id    int64
	site  string
	usage RunLLM
	// started is the run's start: where its video begins.
	started time.Time
	// recorded is whether the box records this run: its requests then name their run and part.
	recorded bool
}

// startRun opens the site's run of kind (register or check) for the operation that just claimed it.
func (s *Service) startRun(ctx context.Context, site, kind string) (*runLog, error) {
	started := s.cfg.Now()
	id, err := s.cfg.Store.StartRun(ctx, site, kind, started)
	if err != nil {
		return nil, err
	}
	l := &runLog{store: s.cfg.Store, now: s.cfg.Now, id: id, site: site, started: started, usage: RunLLM{Priced: true},
		recorded: s.cfg.recordingsOn()}
	s.pruneRecordings(ctx, site)
	s.mu.Lock()
	s.current[site] = id
	s.mu.Unlock()
	return l, nil
}

func (l *runLog) line(kind, text string) {
	if err := l.store.AddLine(context.Background(), l.id, l.now(), kind, text); err != nil {
		slog.Warn("signins: writing a run's log", "site", l.site, "run", l.id, "err", err)
	}
}

// spent counts a browser step's model use. A step that failed never reported its cost: the run's
// count is then only a floor, and says so.
func (l *runLog) spent(reply browser.Message, err error) {
	if err != nil {
		l.usage.Partial = true
	} else if cost, ok := reply["cost"].(map[string]any); ok {
		l.usage.add(cost)
	}
	if err := l.store.SetUsage(context.Background(), l.id, l.usage); err != nil {
		slog.Warn("signins: writing a run's model use", "site", l.site, "run", l.id, "err", err)
	}
}

// box is a line in the engine's own words.
func (l *runLog) box(format string, args ...any) { l.line("box", fmt.Sprintf(format, args...)) }

// progress is the worker's step_progress for this run: how each screen is done (its recorded path
// replayed, or the model's), each step's goal, each action as it ended, the site's email's link opened
// or code typed (said in the conversation, as "An email arrived" is), and a model answer that could not be used (else the log goes quiet while browser-use asks again).
// `done` is left out: the box's next line says what the browser reported. Anything else the worker
// sends (trace_error …) is its own evidence, not the owner's log.
func (l *runLog) progress(event string, p browser.Message) {
	switch event {
	case "goal":
		if g, _ := p["goal"].(string); g != "" {
			l.line("goal", g)
		}
	case "action":
		if kind, _ := p["kind"].(string); kind != "done" {
			l.line("action", actionText(p))
			x, hasX := p["x"].(float64)
			y, hasY := p["y"].(float64)
			if hasX && hasY {
				l.clicked(int(x), int(y))
			}
		}
	case "prove":
		l.box("%s", provingLine(p))
	case "email_link":
		// The link by the words the model was shown, never its URL: it is the confirmation itself.
		if words, _ := p["words"].(string); words != "" {
			l.box("Opened the email's link “%s”", cut(words, 120))
		} else {
			n, _ := p["n"].(float64)
			l.box("Opened the email's link %d", int(n))
		}
	case "email_code":
		l.box("Typed the code from the email")
	case "recording":
		l.partStarted()
	case "recording_failed":
		detail, _ := p["detail"].(string)
		l.box("This part of the run wasn't recorded: %s", cut(detail, 200))
	case "model_error":
		n, _ := p["failures"].(float64)
		l.box("The model's answer could not be used — asking again (%d in a row)", int(n))
		// What it wrote, when nothing could read it: how the next unknown spelling gets fixed.
		if a, _ := p["answer"].(string); a != "" {
			l.line("answer", a)
		}
	}
}

// actionText is an action as the recorder closed it: what, on which control, how it ended. A fill
// names its field, never the value it typed.
func actionText(p browser.Message) string {
	str := func(k string) string { v, _ := p[k].(string); return v }
	text := str("kind")
	if text == "fill_field" {
		text = "fill"
	}
	if slot := str("slot"); slot != "" {
		text += " " + slot + " into"
	}
	if target := str("target"); target != "" {
		text += " " + target
	}
	switch result := str("result"); result {
	case "":
	case "refused":
		text += " — refused: " + str("rule")
	case "failed", "unknown":
		text += " — " + result
		if e := str("error"); e != "" {
			text += ": " + cut(e, 200)
		}
	default:
		text += " — " + result
	}
	return text
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// end closes the run with the account's state and reason (or ended, when nothing was learnt).
func (l *runLog) end(state, reason string) {
	switch state {
	case "warn":
		l.box("Warn — %s", reason)
	case "ended":
		l.box("Ended — %s", reason)
	}
	if err := l.store.EndRun(context.Background(), l.id, l.now(), state, reason); err != nil {
		slog.Error("signins: ending a run", "site", l.site, "run", l.id, "err", err)
	}
}
