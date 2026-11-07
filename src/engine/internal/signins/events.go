package signins

import (
	"cmp"
	"errors"
	"strings"

	"concierge/engine/internal/browser"
)

// RunEvent is something an agent run tells Config.Events: what it waits on the owner for, or how an
// errand ended.
type RunEvent struct {
	Run int64
	// Kind is "question", "confirm", "human_check" (a wait on the owner) or "done", "not_done",
	// "failed", "stopped" (an errand's end).
	Kind string
	// Text is the question, the captcha's reason, the run's end reason, or the report on done and not_done.
	Text    string
	Options []string
}

// isConfirm is a question the errand asks before the press that finally does the action.
func isConfirm(text string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "confirm:")
}

// questionKind is the kind of a wait on the question's text.
func questionKind(text string) string {
	if isConfirm(text) {
		return "confirm"
	}
	return "question"
}

// emit tells Config.Events. It is never called with s.mu held.
func (s *Service) emit(e RunEvent) {
	if s.cfg.Events != nil {
		s.cfg.Events(e)
	}
}

// Waiting is what an agent run waits on the owner for now: "question", "confirm" (a question that
// starts with "Confirm:"; q is it), "human_check", or "" when nothing.
func (s *Service) Waiting(run int64) (kind string, q *Question) {
	s.mu.Lock()
	e := s.agents[run]
	s.mu.Unlock()
	if e == nil {
		return "", nil
	}
	wait, q, _ := s.agentWaits(Run{ID: run, Site: e.site})
	switch wait {
	case "captcha":
		return "human_check", nil
	case "question":
		return questionKind(q.Text), q
	}
	return "", nil
}

// endEvent is how an errand's run ended, from the worker's reply, its error and why it ended early
// (the reasons the run's log gives); stopping is the engine going down.
func endEvent(run int64, reply browser.Message, err error, ended string, stopping bool) RunEvent {
	report, _ := reply["report"].(string)
	failed := func(why string) RunEvent { return RunEvent{Run: run, Kind: "failed", Text: why} }
	switch {
	case ended == "stopped":
		return RunEvent{Run: run, Kind: "stopped", Text: ended}
	case ended != "":
		return failed(ended)
	case stopping:
		return failed("the box restarted")
	case errors.Is(err, browser.ErrUnavailable):
		return failed("the browser worker was away")
	case err != nil:
		return failed("the browser could not finish the run")
	case reply["success"] == true:
		return RunEvent{Run: run, Kind: "done", Text: report}
	case reply["outcome"] == "done":
		return RunEvent{Run: run, Kind: "not_done", Text: report}
	}
	reason, _ := reply["error"].(string)
	return failed(cmp.Or(reason, "the run did not finish"))
}
