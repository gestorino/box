package signins

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"concierge/engine/internal/browser"
)

// An agent run is the worker's agent (agent.py) doing an action at a link in the site's browser, by
// the rules of the run's kind: an errand (errands.go), a registration (register.go) or a sign-in
// check (accounts.go). It runs on the sign-in run machinery: it claims the site, is a run with its
// log, recording and live view, asks the owner through questions and captchas, and on the box's
// account receives the site's SMS codes and emails.

// ErrBadAnswer refuses an empty answer to a run's question.
var ErrBadAnswer = errors.New("an answer needs to say something")

// questionWait is how long a question waits on the owner before the run ends.
const questionWait = time.Hour

// Question is what an agent run asks the owner: the page's question and the choices it offers. Wait
// names this one wait, unique in the service: AnswerWait answers it only while it is the run's
// current one.
type Question struct {
	Text    string   `json:"text"`
	Options []string `json:"options"`
	Wait    int64    `json:"-"`
}

// Served is one model and provider's share of an agent run: calls and OpenRouter's billed cost.
type Served struct {
	Model    string  `json:"model"`
	Provider string  `json:"provider"`
	Calls    int     `json:"calls"`
	CostUSD  float64 `json:"cost_usd"`
}

// agentRun is an agent run in flight: how to stop it, and the question it waits on.
type agentRun struct {
	site   string
	cancel context.CancelFunc
	// On the box's account (or creating it): the account's address, where the site's emails arrive,
	// and the run's SMS listener; "" and nil otherwise. since is the run's start: only an email stored
	// after it is the site's to this run, and emails counts the ones the worker has taken.
	email  string
	sms    *codeWait
	since  time.Time
	emails int
	// done and notDone are how the run ends when the model finishes, with and without the action.
	done, notDone string
	// proved, when set, is told the footprint the worker proved (nil: none) when the model finished
	// with the action, before the run ends (a sign-in's).
	proved   func(ctx context.Context, log *runLog, footprint any)
	question *Question
	answer   chan string
	until    time.Time
	// why it ended early, when it did: "stopped" or "no answer".
	ended string
	// what its model calls cost, by model and provider in the order first served, as the worker told
	// each one: known however the run ends.
	spent []modelSpend
}

type modelSpend struct {
	Served
	in, out int
}

// told adds one model call the worker told (model_call) to what the run spent.
func (e *agentRun) told(p browser.Message) {
	model, _ := p["model"].(string)
	provider, _ := p["provider"].(string)
	cost, _ := p["cost_usd"].(float64)
	in, _ := p["input_tokens"].(float64)
	out, _ := p["output_tokens"].(float64)
	i := slices.IndexFunc(e.spent, func(s modelSpend) bool { return s.Model == model && s.Provider == provider })
	if i < 0 {
		e.spent = append(e.spent, modelSpend{Served: Served{Model: model, Provider: provider}})
		i = len(e.spent) - 1
	}
	s := &e.spent[i]
	s.Calls++
	s.CostUSD += cost
	s.in += int(in)
	s.out += int(out)
}

// agentStart is what launch starts: an agent run of kind (errand, register, check) at url on the site
// key, in its profile session ("" a throwaway) with the owner's values (nil none). free, when set,
// runs first and may end the run without the model (true); prove, when set, asks the worker to prove
// a sign-in's footprint (proofs, old), and proved is agentRun's; record asks the worker to read what a
// done run arranged (appointment, appointments; "" nothing); finish, when set, is told how the run
// ended and may answer a next step, started once the site is released.
type agentStart struct {
	key, kind, url, action, session string
	record                          string
	domains                         []string
	values                          map[string]string
	email                           string
	done, notDone                   string
	free                            func(ctx context.Context, e *agentRun, log *runLog) bool
	prove                           browser.Message
	proved                          func(ctx context.Context, log *runLog, footprint any)
	finish                          func(ctx context.Context, run int64, reply browser.Message, err error, ended string) (next func())
}

// launch starts an agent run on a site the caller has claimed, and releases the site when the run
// has ended (or when it couldn't start).
func (s *Service) launch(ctx context.Context, st agentStart) (int64, error) {
	log, err := s.startRun(ctx, st.key, st.kind)
	if err != nil {
		s.release(st.key)
		return 0, err
	}
	if err := s.cfg.Store.AddAgentRun(ctx, log.id, st.url, st.action); err != nil {
		log.end("ended", "the run could not be recorded")
		s.release(st.key)
		return 0, err
	}
	runCtx, cancel := context.WithCancel(s.cfg.Base)
	e := &agentRun{site: st.key, cancel: cancel, email: st.email, since: log.started, done: st.done, notDone: st.notDone,
		proved: st.proved}
	s.mu.Lock()
	s.agents[log.id] = e
	s.mu.Unlock()
	s.runs.Add(1)
	go func() {
		defer s.runs.Done()
		if next := s.runLaunched(runCtx, cancel, e, log, st); next != nil {
			next()
		}
	}()
	return log.id, nil
}

// runLaunched is a launched run to its end, the site released after it; it answers finish's next step.
func (s *Service) runLaunched(ctx context.Context, cancel context.CancelFunc, e *agentRun, log *runLog, st agentStart) func() {
	defer s.release(st.key)
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.agents, log.id)
		s.mu.Unlock()
	}()
	if st.free != nil && st.free(ctx, e, log) {
		return nil
	}
	request := browser.Message{"url": st.url, "action": st.action, "kind": st.kind, "site": st.key,
		"session": st.session, "allowed_domains": st.domains}
	if st.prove != nil {
		request["prove"] = st.prove
	}
	if st.record != "" {
		request["record"] = st.record
	}
	if st.values != nil {
		request["values"] = st.values
		// Before the worker starts: the site may send its code the moment the form is sent.
		if s.cfg.SMS != nil {
			e.sms = openCodes(ctx, s.cfg.SMS, s.caughtUp)
		}
	}
	reply, err := s.runAgent(ctx, e, log, request)
	if st.finish == nil {
		return nil
	}
	s.mu.Lock()
	ended := e.ended
	s.mu.Unlock()
	return st.finish(ctx, log.id, reply, err, ended)
}

func (s *Service) runAgent(ctx context.Context, e *agentRun, log *runLog, request browser.Message) (browser.Message, error) {
	log.box("Doing “%s” at %s", request["action"], request["url"])
	maps.Copy(request, log.record())
	progress := func(event string, p browser.Message) {
		if event != "model_call" {
			log.progress(event, p)
			return
		}
		s.mu.Lock()
		e.told(p)
		s.mu.Unlock()
	}
	reply, err := s.cfg.Worker.RequestAsk(ctx, "run_agent", request, s.agentAsk(e, log), progress)
	log.partEnded()
	s.mu.Lock()
	spent := slices.Clone(e.spent)
	s.mu.Unlock()
	served := make([]Served, 0, len(spent))
	for _, c := range spent {
		served = append(served, c.Served)
		log.usage.add(map[string]any{"model": c.Model, "llm_calls": float64(c.Calls), "billed_usd": c.CostUSD,
			"input_tokens": float64(c.in), "output_tokens": float64(c.out)})
	}
	log.spent(nil, err) // the reply carries no cost: it was told call by call
	report, _ := reply["report"].(string)
	if err := s.cfg.Store.FinishAgentRun(context.WithoutCancel(ctx), log.id, report, served); err != nil {
		slog.Error("signins: storing an agent run's result", "run", log.id, "err", err)
	}
	s.mu.Lock()
	ended := e.ended
	s.mu.Unlock()
	switch {
	case ended != "":
		log.end("ended", ended)
	case ctx.Err() != nil:
		// The engine is stopping (a restart, a deploy): the browser was ended under the run, it didn't fail.
		log.end("ended", "the box restarted")
	case errors.Is(err, browser.ErrUnavailable):
		log.end("ended", "the browser worker was away")
	case err != nil:
		log.end("warn", "the browser could not finish the run")
	case reply["success"] == true:
		if e.proved != nil {
			e.proved(ctx, log, reply["footprint"])
		}
		log.end("ended", e.done)
	case reply["outcome"] == "done":
		// The model finished without the action: the owner's own "no", or a site that couldn't (nothing
		// there to act on). Its report's first sentence says which; nothing went wrong for the owner to
		// look into.
		report, _ := reply["report"].(string)
		log.end("ended", whyNot(e.notDone, report))
	default:
		reason, _ := reply["error"].(string) // the worker's own, such as "no OpenRouter key"
		log.end("warn", cmp.Or(reason, "the run did not finish"))
	}
	return reply, err
}

// agentAsk answers the run's asks: a question waits on the owner's answer (an hour at most, then
// the run ends), a human check waits for the owner to solve it, and on the box's account an SMS
// code or the site's email waits for the box to receive it.
func (s *Service) agentAsk(e *agentRun, log *runLog) browser.Ask {
	return func(ctx context.Context, q browser.AskRequest) (string, bool) {
		switch q.Name {
		case "sms_code":
			return s.smsCode(ctx, e, log)
		case "email":
			return s.nextEmail(ctx, e, log)
		case "human_check":
			done, ok := s.humanCheck(ctx, e.site, log, func() {
				s.emit(RunEvent{Run: log.id, Kind: "human_check", Text: q.Why})
			})
			if !ok && ctx.Err() == nil {
				// Given up or timed out: the run ends.
				s.mu.Lock()
				e.ended = "the captcha wasn't solved"
				s.mu.Unlock()
				e.cancel()
			}
			return done, ok
		case "question":
		default:
			return "", false
		}
		answer := make(chan string, 1)
		s.mu.Lock()
		s.waitSeq++
		wait := s.waitSeq
		e.question, e.answer, e.until = &Question{Text: q.Why, Options: q.Options, Wait: wait}, answer, s.cfg.Now().Add(questionWait)
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			e.question, e.answer = nil, nil
			s.mu.Unlock()
		}()
		log.line("question", q.Why) // its choices are the question's own, shown with it while it waits
		s.emit(RunEvent{Run: log.id, Kind: questionKind(q.Why), Text: q.Why, Options: q.Options, Wait: wait})
		select {
		case a := <-answer:
			log.line("reply", a)
			return a, true
		case <-s.cfg.After(questionWait):
			s.mu.Lock()
			e.ended = "no answer"
			s.mu.Unlock()
			e.cancel()
			return "", false
		case <-ctx.Done():
			return "", false
		}
	}
}

// Answer gives the owner's answer to the question an agent run waits on.
func (s *Service) Answer(run int64, answer string) error { return s.answer(run, 0, answer) }

// ErrWaitChanged refuses an answer to a wait that is no longer the run's current one.
var ErrWaitChanged = errors.New("the errand now waits on something else")

// AnswerWait answers the run's question only while wait (Question.Wait) is the one it waits on.
func (s *Service) AnswerWait(run, wait int64, answer string) error {
	if wait == 0 {
		return ErrWaitChanged
	}
	return s.answer(run, wait, answer)
}

// answer answers the run's current question; wait, when not 0, must be its wait.
func (s *Service) answer(run, wait int64, answer string) error {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return ErrBadAnswer
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.agents[run]
	if e == nil || e.answer == nil {
		return ErrNotWaiting
	}
	if wait != 0 && e.question.Wait != wait {
		return ErrWaitChanged
	}
	e.answer <- answer
	e.answer = nil
	return nil
}

// Stop ends an agent run in flight.
func (s *Service) Stop(run int64) error {
	s.mu.Lock()
	e := s.agents[run]
	if e != nil && e.ended == "" {
		e.ended = "stopped"
	}
	s.mu.Unlock()
	if e == nil {
		return ErrNotWaiting
	}
	e.cancel()
	return nil
}

// agentWaits is what the agent run waits on now, and until when.
func (s *Service) agentWaits(r Run) (string, *Question, *time.Time) {
	if wait, until := s.waitOf(r.Site, r.ID); wait == waitCaptcha {
		return "captcha", nil, &until
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.agents[r.ID]; e != nil && e.question != nil {
		q, until := *e.question, e.until
		return "question", &q, &until
	}
	return "", nil, nil
}

// agentLog is where the worker writes an agent run's browser-use log: beside its video, so it goes
// with the run.
func (s *Service) agentLog(site string, run int64) string {
	return filepath.Join(runDir(s.cfg.RecordingsDir, site, run), "browser-use.log")
}

// AgentRunView is an errand in the list: its run, link and action, and what it waits on
// ("question", "captcha", or "" when nothing).
type AgentRunView struct {
	Run
	URL     string `json:"url"`
	Action  string `json:"action"`
	Waiting string `json:"waiting,omitempty"`
}

// AgentRunDetail is one errand's page: the view, its log (questions and the owner's replies among the lines), its
// recording, the model's report, the cost by provider, the question it waits on, and whether its
// screen may be watched.
type AgentRunDetail struct {
	AgentRunView
	Lines        []RunLine  `json:"lines"`
	Parts        []RunPart  `json:"parts"`
	Report       string     `json:"report"`
	Served       []Served   `json:"served"`
	Question     *Question  `json:"question,omitempty"`
	WaitingUntil *time.Time `json:"waiting_until,omitempty"`
	Screen       bool       `json:"screen"`
	// Log says the run has a browser-use log to open: one the model never ran in (a sign-in check
	// read without it) has none.
	Log bool `json:"log"`
	// Appointment is the appointment the run made (appointments.go); 0 when none.
	Appointment int64 `json:"appointment,omitempty"`
}

// AgentRuns is the kept agent runs of every kind, newest first.
func (s *Service) AgentRuns(ctx context.Context) ([]AgentRunView, error) {
	views, err := s.cfg.Store.AgentRuns(ctx)
	for i := range views {
		views[i].Waiting, _, _ = s.agentWaits(views[i].Run)
	}
	return views, err
}

// AgentRun is one agent run's page; found is false for a run that is none.
func (s *Service) AgentRun(ctx context.Context, run int64) (AgentRunDetail, bool, error) {
	view, report, served, found, err := s.cfg.Store.AgentRun(ctx, run)
	if err != nil || !found {
		return AgentRunDetail{}, found, err
	}
	_, lines, _, err := s.cfg.Store.Run(ctx, view.Site, run)
	if err != nil {
		return AgentRunDetail{}, false, err
	}
	parts, err := s.Parts(ctx, view.Run)
	if err != nil {
		return AgentRunDetail{}, false, err
	}
	d := AgentRunDetail{AgentRunView: view, Lines: lines, Parts: parts, Report: report, Served: served,
		Screen: s.cfg.ScreensDir != "" && view.State == "running"}
	d.Waiting, d.Question, d.WaitingUntil = s.agentWaits(view.Run)
	_, d.Log = s.logOf(view.Run)
	if d.Appointment, err = s.cfg.Store.AppointmentOfRun(ctx, run); err != nil {
		return AgentRunDetail{}, false, err
	}
	return d, true, nil
}

// AgentRunLog is the run's browser-use log file, when it has one.
func (s *Service) AgentRunLog(ctx context.Context, run int64) (string, bool, error) {
	view, _, _, found, err := s.cfg.Store.AgentRun(ctx, run)
	if err != nil || !found {
		return "", false, err
	}
	path, has := s.logOf(view.Run)
	return path, has, nil
}

// logOf is where the run's browser-use log is, and whether it is there.
func (s *Service) logOf(r Run) (string, bool) {
	if !s.cfg.recordingsOn() {
		return "", false
	}
	path := s.agentLog(r.Site, r.ID)
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}

// AddAgentRun records an agent run's link and action.
func (s *Store) AddAgentRun(ctx context.Context, run int64, link, action string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_runs (run, url, action) VALUES (?, ?, ?)`, run, link, action)
	return err
}

// FinishAgentRun records the model's report and the cost by provider (none: an empty list).
func (s *Store) FinishAgentRun(ctx context.Context, run int64, report string, served []Served) error {
	if served == nil {
		served = []Served{}
	}
	raw, err := json.Marshal(served)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE agent_runs SET report = ?, served_json = ? WHERE run = ?`, report, string(raw), run)
	return err
}

const agentColumns = runColumns + `, e.url, e.action, e.report, e.served_json`
const agentTables = runTables + ` JOIN agent_runs e ON e.run = r.id`

func scanAgentRun(scan func(...any) error) (AgentRunView, string, []Served, error) {
	var v AgentRunView
	var report, servedJSON string
	r, err := scanRun(func(dest ...any) error {
		return scan(append(dest, &v.URL, &v.Action, &report, &servedJSON)...)
	})
	if err != nil {
		return AgentRunView{}, "", nil, err
	}
	v.Run = r
	served := []Served{}
	if err := json.Unmarshal([]byte(servedJSON), &served); err != nil {
		return AgentRunView{}, "", nil, err
	}
	if served == nil { // a free check stored before FinishAgentRun wrote an empty list
		served = []Served{}
	}
	return v, report, served, nil
}

// AgentRuns is the kept agent runs across sites, newest first.
func (s *Store) AgentRuns(ctx context.Context) ([]AgentRunView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentColumns+agentTables+` ORDER BY r.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentRunView{}
	for rows.Next() {
		v, _, _, err := scanAgentRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// AgentRun is one agent run with its report and cost by provider; found is false for a run that is none.
func (s *Store) AgentRun(ctx context.Context, run int64) (AgentRunView, string, []Served, bool, error) {
	v, report, served, err := scanAgentRun(s.db.QueryRowContext(ctx, `SELECT `+agentColumns+agentTables+` WHERE r.id = ?`, run).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentRunView{}, "", nil, false, nil
	}
	if err != nil {
		return AgentRunView{}, "", nil, false, err
	}
	return v, report, served, true, nil
}

// whyNot is how a run that ended without its action says so: its kind's words, and the model's first
// sentence of why ("not done: There are no appointments to cancel.").
func whyNot(notDone, report string) string {
	why := oneLine(report)
	if i := strings.Index(why, ". "); i >= 0 {
		why = why[:i+1]
	}
	if why == "" {
		return notDone
	}
	return notDone + ": " + cut(why, 160)
}
