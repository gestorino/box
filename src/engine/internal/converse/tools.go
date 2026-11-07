package converse

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"unicode"

	agentv1 "concierge/engine/internal/gen/agent/v1"
	"concierge/engine/internal/secrets"
	"concierge/engine/internal/signins"
)

// Errands is what the tools need of signins.Service.
type Errands interface {
	Cards(ctx context.Context) ([]signins.Card, error)
	Registered(ctx context.Context, key string) (bool, error)
	StartErrand(ctx context.Context, e signins.Errand) (int64, error)
	AnswerWait(run, wait int64, answer string) error
	Stop(run int64) error
	AgentRun(ctx context.Context, run int64) (signins.AgentRunDetail, bool, error)
	Waiting(run int64) (string, *signins.Question)
	Appointments(ctx context.Context, site string) ([]signins.Appointment, error)
	CheckAppointment(ctx context.Context, id int64) (int64, error)
}

var _ Errands = (*signins.Service)(nil)

// Tools are what the agent may call: each a plain call into the engine's own services, every gate
// left where it is. A call's result is compact JSON; anything refused or failed is {"error": "…"}.
type Tools struct {
	errands Errands
	store   *Store
	tools   []tool
}

type tool struct {
	spec *agentv1.Tool
	run  func(t *Tools, ctx context.Context, conv, taking int64, args string) (any, error)
}

func NewTools(e Errands, s *Store) *Tools {
	return &Tools{errands: e, store: s, tools: []tool{
		{&agentv1.Tool{Name: "list_accounts",
			Description: "The sites where the box has its own account: each site's key, name and link, and the actions an " +
				"errand can do there with the details each action takes. An errand starts only on one of these sites, " +
				"with one of its actions.",
			ParametersJson: `{"type":"object","properties":{},"required":[],"additionalProperties":false}`},
			(*Tools).listAccounts},
		{&agentv1.Tool{Name: "start_errand",
			Description: "Start an errand: the box does an action on one of its accounts' sites, in its browser. site is a " +
				"site key from list_accounts; action is one of that site's actions, word for word; details are the " +
				"action's details the owner gave, each {name, value} with the detail's name as listed (leave out any " +
				"the owner did not give); appointment is the id, from list_appointments, of the appointment an action " +
				"that changes or cancels one is about, else 0. Returns the errand's run. What the errand asks, its " +
				"Confirm and its end come back as errand events.",
			ParametersJson: `{"type":"object","properties":{"site":{"type":"string"},"action":{"type":"string"},` +
				`"details":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"value":{"type":"string"}},` +
				`"required":["name","value"],"additionalProperties":false}},"appointment":{"type":"integer"}},` +
				`"required":["site","action","details","appointment"],"additionalProperties":false}`},
			(*Tools).startErrand},
		{&agentv1.Tool{Name: "answer_errand",
			Description: "Answer what an errand of this conversation waits on; never with words of your own. A question: " +
				"answer is one of its options, word for word, the one the owner chose, and owner_event is 0; or " +
				"owner_event is the # of the owner's message that answers it, and answer is \"\". A question with no " +
				"options takes only owner_event. A Confirm (a question that starts \"Confirm:\"): answer must be \"\" and " +
				"owner_event the # of the owner's own message, sent after the Confirm was told, that answers it. The " +
				"owner's words go to the errand as they are. A Confirm takes one answer.",
			ParametersJson: `{"type":"object","properties":{"run":{"type":"integer"},"answer":{"type":"string"},` +
				`"owner_event":{"type":"integer"}},"required":["run","answer","owner_event"],"additionalProperties":false}`},
			(*Tools).answerErrand},
		{&agentv1.Tool{Name: "stop_errand",
			Description:    "Stop an errand of this conversation.",
			ParametersJson: runSchema},
			(*Tools).stopErrand},
		{&agentv1.Tool{Name: "errand_status",
			Description: "An errand of this conversation now: its state, what it waits on (question, confirm, human_check, " +
				"or \"\" for nothing), that question and its options, and its report once it has ended.",
			ParametersJson: runSchema},
			(*Tools).errandStatus},
		{&agentv1.Tool{Name: "list_appointments",
			Description: "The box's appointments, upcoming first: each one's id, site, title, date (YYYY-MM-DD), time " +
				"(HH:MM), place and status (upcoming, cancelled or not found); \"\" where the site did not say.",
			ParametersJson: `{"type":"object","properties":{},"required":[],"additionalProperties":false}`},
			(*Tools).listAppointments},
		{&agentv1.Tool{Name: "check_appointment",
			Description: "Start an errand that looks on its site whether an appointment from list_appointments is still " +
				"there: id is the appointment's id. Returns the errand's run.",
			ParametersJson: `{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"],"additionalProperties":false}`},
			(*Tools).checkAppointment},
	}}
}

const runSchema = `{"type":"object","properties":{"run":{"type":"integer"}},"required":["run"],"additionalProperties":false}`

// Specs is the tool list a step offers the agent.
func (t *Tools) Specs() []*agentv1.Tool {
	out := make([]*agentv1.Tool, len(t.tools))
	for i, tl := range t.tools {
		out[i] = tl.spec
	}
	return out
}

// Waits is the run still waiting on what an errand's event told: a wait of that kind and, for a
// question or a Confirm, that wait itself. An event whose wait ended (answered in the console, or the
// run gone with a restart) asks nothing any more.
func (t *Tools) Waits(run int64, kind string, wait int64) bool {
	now, q := t.errands.Waiting(run)
	if now != kind {
		return false
	}
	return kind == "human_check" || (q != nil && q.Wait == wait)
}

// Running is the run still running: what keeps the conversation that started it open.
func (t *Tools) Running(ctx context.Context, run int64) (bool, error) {
	d, found, err := t.errands.AgentRun(ctx, run)
	if err != nil {
		return false, err
	}
	return found && d.State == "running", nil
}

// refusal is a tool's own refusal, in plain words the agent reads.
type refusal string

func (r refusal) Error() string { return string(r) }

func refuse(format string, args ...any) error { return refusal(fmt.Sprintf(format, args...)) }

// serviceWords are the services' errors whose own words are plain enough for the agent; "" keeps
// the error's text (the signins errors wrapped with %w carry plain words of their own).
var serviceWords = []struct {
	err   error
	words string
}{
	{signins.ErrNotWaiting, "the errand is not waiting for an answer"},
	{signins.ErrWaitChanged, "the errand now waits on something else: wait until it is told"},
	{signins.ErrBadAnswer, ""},
	{signins.ErrBusy, ""},
	{signins.ErrNoKey, ""},
	{signins.ErrShuttingDown, ""},
	{signins.ErrBadErrand, ""},
	{signins.ErrNoPassword, ""},
	{signins.ErrNoAccount, ""},
	{signins.ErrUnknownSite, ""},
	{signins.ErrWaitingForWiFi, ""},
	{signins.ErrNoAppointment, ""},
	{secrets.ErrUnavailable, "secrets unavailable"}, // its wrapped text may name a secret
}

// plainWords is what a failed call says: its refusal, a service's plain error, or, for anything
// else (logged), that something went wrong.
func plainWords(name string, err error) string {
	var r refusal
	if errors.As(err, &r) {
		return string(r)
	}
	for _, w := range serviceWords {
		if errors.Is(err, w.err) {
			return cmp.Or(w.words, err.Error())
		}
	}
	slog.Error("converse: a tool failed", "tool", name, "err", err)
	return "something went wrong"
}

// Call runs one tool call of the conversation's agent, in the turn taking the event #taking, and
// returns its result: compact JSON, an error a result of its own, {"error": "<plain words>"}. The
// turn's agent was shown no event still pending after #taking.
func (t *Tools) Call(ctx context.Context, conv, taking int64, call *agentv1.ToolCall) string {
	result, err := t.call(ctx, conv, taking, call)
	if err != nil {
		result = struct {
			Error string `json:"error"`
		}{plainWords(call.GetName(), err)}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return `{"error":"the result could not be written"}`
	}
	return string(raw)
}

func (t *Tools) call(ctx context.Context, conv, taking int64, call *agentv1.ToolCall) (any, error) {
	if call == nil {
		return nil, refuse("no tool was called")
	}
	for _, tl := range t.tools {
		if tl.spec.GetName() == call.GetName() {
			return tl.run(t, ctx, conv, taking, call.GetArgumentsJson())
		}
	}
	return nil, refuse("there is no tool %q", call.GetName())
}

// field is one argument of a call: its name, where it is decoded and the words for its type.
type field struct {
	name string
	into any
	is   string
}

// parseObject decodes raw, a JSON object that must have exactly fields, each of its type; where
// names it in the error.
func parseObject(raw []byte, where string, fields ...field) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return refuse("%s must be one JSON object", where)
	}
	if _, err := dec.Token(); err != io.EOF {
		return refuse("%s must be one JSON object", where)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if !slices.ContainsFunc(fields, func(f field) bool { return f.name == k }) {
			return refuse("%s: there is no argument %q", where, k)
		}
	}
	for _, f := range fields {
		v, ok := obj[f.name]
		if !ok {
			return refuse("%s: %s is missing", where, f.name)
		}
		if string(v) == "null" || json.Unmarshal(v, f.into) != nil {
			return refuse("%s: %s must be %s", where, f.name, f.is)
		}
	}
	return nil
}

func parseArgs(args string, fields ...field) error {
	return parseObject([]byte(args), "the arguments", fields...)
}

func integer(name string, into *int64) field { return field{name, into, "an integer"} }
func text(name string, into *string) field   { return field{name, into, "a string"} }

// runArg is the run of a call that names one, which must be one this conversation started.
func (t *Tools) runArg(ctx context.Context, conv int64, args string, more ...field) (int64, error) {
	var run int64
	if err := parseArgs(args, append([]field{integer("run", &run)}, more...)...); err != nil {
		return 0, err
	}
	of, ok, err := t.store.ConversationOfRun(ctx, run)
	if err != nil {
		return 0, err
	}
	if !ok || of != conv {
		return 0, refuse("run %d is not an errand of this conversation", run)
	}
	return run, nil
}

type accountOut struct {
	Site    string      `json:"site"`
	Name    string      `json:"name"`
	Link    string      `json:"link"`
	Actions []actionOut `json:"actions"`
}

type actionOut struct {
	Text    string   `json:"text"`
	Details []string `json:"details"`
}

// accounts is the cards of the sites where the box has its registered account.
func (t *Tools) accounts(ctx context.Context) ([]signins.Card, error) {
	cards, err := t.errands.Cards(ctx)
	if err != nil {
		return nil, err
	}
	out := []signins.Card{}
	for _, c := range cards {
		ok, err := t.errands.Registered(ctx, c.Key)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (t *Tools) listAccounts(ctx context.Context, _, _ int64, args string) (any, error) {
	if err := parseArgs(args); err != nil {
		return nil, err
	}
	cards, err := t.accounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]accountOut, 0, len(cards))
	for _, c := range cards {
		a := accountOut{Site: c.Key, Name: c.Name, Link: c.Link, Actions: make([]actionOut, 0, len(c.Actions))}
		for _, act := range c.Actions {
			a.Actions = append(a.Actions, actionOut{Text: act.Text, Details: append([]string{}, act.Params...)})
		}
		out = append(out, a)
	}
	return struct {
		Accounts []accountOut `json:"accounts"`
	}{out}, nil
}

// runOut is a call's result that started an errand.
type runOut struct {
	Run int64 `json:"run"`
}

// jsTrim trims what JavaScript's String.prototype.trim does: the console's own rule (withDetails).
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r == '\uFEFF' || (r != '\u0085' && unicode.IsSpace(r)) })
}

// withDetails is the console's errand action (src/ui/src/pages/errands.tsx, withDetails): "<action>
// (<detail>: <value>; …)" in the action's own order, the empty ones left out.
func withDetails(a signins.Action, values map[string]string) string {
	var said []string
	for _, p := range a.Params {
		if v := jsTrim(values[p]); v != "" {
			said = append(said, p+": "+v)
		}
	}
	if len(said) == 0 {
		return a.Text
	}
	return a.Text + " (" + strings.Join(said, "; ") + ")"
}

func (t *Tools) startErrand(ctx context.Context, conv, _ int64, args string) (any, error) {
	var site, action string
	var details []json.RawMessage
	var appointment int64
	if err := parseArgs(args, text("site", &site), text("action", &action),
		field{"details", &details, "a list of {name, value}"}, integer("appointment", &appointment)); err != nil {
		return nil, err
	}
	cards, err := t.accounts(ctx)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(cards, func(c signins.Card) bool { return c.Key == site })
	if i < 0 {
		return nil, refuse("%q is not a site of the box's accounts: list_accounts has them", site)
	}
	card := cards[i]
	j := slices.IndexFunc(card.Actions, func(a signins.Action) bool { return a.Text == strings.TrimSpace(action) })
	if j < 0 {
		return nil, refuse("%q is not an action of %s: list_accounts has its actions", action, site)
	}
	act := card.Actions[j]
	values := map[string]string{}
	for k, raw := range details {
		var name, value string
		if err := parseObject(raw, fmt.Sprintf("details[%d]", k), text("name", &name), text("value", &value)); err != nil {
			return nil, err
		}
		if !slices.Contains(act.Params, name) {
			return nil, refuse("%q is not a detail of %q: its details are %s", name, act.Text, strings.Join(act.Params, ", "))
		}
		if _, twice := values[name]; twice {
			return nil, refuse("the detail %q is given twice", name)
		}
		values[name] = value
	}
	if appointment < 0 {
		return nil, refuse("appointment is an appointment's id, or 0 for none")
	}
	run, err := t.errands.StartErrand(ctx, signins.Errand{URL: card.Link, Action: withDetails(act, values),
		Effect: act.Effect, Appointment: appointment})
	if err != nil {
		return nil, err
	}
	return t.linked(ctx, conv, run)
}

// linked links a run the conversation started; a run it can't link is stopped, since no answer or
// event of it could reach the owner.
func (t *Tools) linked(ctx context.Context, conv, run int64) (any, error) {
	if err := t.store.LinkRun(ctx, conv, run); err != nil {
		slog.Error("converse: linking a run", "conversation", conv, "run", run, "err", err)
		t.errands.Stop(run)
		return nil, refuse("the errand could not be kept with this conversation, so it was stopped")
	}
	return runOut{run}, nil
}

// answered is an answer_errand's result.
var answered = struct {
	Answered bool `json:"answered"`
}{true}

func (t *Tools) answerErrand(ctx context.Context, conv, taking int64, args string) (any, error) {
	var answer string
	var ownerEvent int64
	run, err := t.runArg(ctx, conv, args, text("answer", &answer), integer("owner_event", &ownerEvent))
	if err != nil {
		return nil, err
	}
	kind, q := t.errands.Waiting(run)
	if (kind == "question" || kind == "confirm") && (q == nil || q.Wait == 0) {
		return nil, refuse("run %d is not waiting for an answer", run)
	}
	switch kind {
	case "confirm":
		return t.answerConfirm(ctx, conv, taking, run, q.Wait, answer, ownerEvent)
	case "question":
		if (answer == "") == (ownerEvent == 0) {
			return nil, refuse("a question takes either answer or owner_event, not both and not neither")
		}
		if ownerEvent != 0 {
			if answer, err = t.ownerWords(ctx, conv, taking, ownerEvent); err != nil {
				return nil, err
			}
		} else if answer, err = offered(q, answer); err != nil {
			return nil, err
		}
		if err := t.errands.AnswerWait(run, q.Wait, answer); err != nil {
			return nil, err
		}
		return answered, nil
	case "human_check":
		return nil, refuse("run %d waits for a human check, which the owner does in the console, not for an answer", run)
	}
	return nil, refuse("run %d is not waiting for an answer", run)
}

// offered is the question's own option the agent's answer names, word for word once trimmed: the
// agent's own words never reach an errand. A question with no options takes the owner's words only.
func offered(q *signins.Question, answer string) (string, error) {
	if len(q.Options) == 0 {
		return "", refuse("this question offers no options: answer must be \"\" and owner_event the # of the owner's " +
			"message that answers it")
	}
	i := slices.IndexFunc(q.Options, func(o string) bool { return jsTrim(o) == jsTrim(answer) })
	if i < 0 {
		return "", refuse("answer must be one of its options, word for word, or \"\" with owner_event the # of the " +
			"owner's message that answers it")
	}
	return q.Options[i], nil
}

// ownerWords is the text of the owner's message #id in this conversation, one the turn taking the
// event #taking was shown: a message still pending after it is its own turn's, not yet shown.
func (t *Tools) ownerWords(ctx context.Context, conv, taking, id int64) (string, error) {
	e, pending, found, err := t.store.event(ctx, conv, id)
	if err != nil {
		return "", err
	}
	o, ok := e.GetKind().(*agentv1.Event_Owner)
	if !found || !ok {
		return "", refuse("#%d is not a message of the owner's in this conversation", id)
	}
	if pending && id > taking {
		return "", refuse("#%d is a message of the owner's you were not shown yet: it gets its own turn", id)
	}
	return o.Owner, nil
}

// answerConfirm is the Confirm gate: only the owner's own message, sent after the Confirm was told in
// this conversation, answers it, verbatim and once, and only while that Confirm (its wait) is the one
// the errand waits on.
func (t *Tools) answerConfirm(ctx context.Context, conv, taking, run, wait int64, answer string, ownerEvent int64) (any, error) {
	if answer != "" || ownerEvent <= 0 {
		return nil, refuse("a Confirm takes no answer of yours: answer must be \"\" and owner_event the # of the " +
			"owner's own message that answers it")
	}
	confirm, toldWait, toldAt, ok, err := t.store.pendingConfirm(ctx, conv, run)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, refuse("run %d's Confirm was not told in this conversation, or was answered already", run)
	}
	if toldWait != wait {
		return nil, refuse("the Confirm told (#%d) is no longer the one run %d waits on: wait until its new one is told", confirm, run)
	}
	if ownerEvent <= toldAt {
		return nil, refuse("#%d came before the Confirm (#%d) was told: only the owner's message after it answers it", ownerEvent, confirm)
	}
	words, err := t.ownerWords(ctx, conv, taking, ownerEvent)
	if err != nil {
		return nil, err
	}
	if err := t.store.claimConfirm(ctx, conv, run, confirm, ownerEvent); err != nil {
		return nil, err
	}
	if err := t.errands.AnswerWait(run, wait, words); err != nil {
		if uerr := t.store.unclaimConfirm(ctx, conv, run, ownerEvent); uerr != nil {
			slog.Error("converse: letting go of a Confirm's claim", "conversation", conv, "run", run, "err", uerr)
		}
		return nil, err
	}
	return answered, nil
}

func (t *Tools) stopErrand(ctx context.Context, conv, _ int64, args string) (any, error) {
	run, err := t.runArg(ctx, conv, args)
	if err != nil {
		return nil, err
	}
	if err := t.errands.Stop(run); err != nil {
		return nil, err
	}
	return struct {
		Stopped bool `json:"stopped"`
	}{true}, nil
}

type statusOut struct {
	State    string   `json:"state"`
	Waiting  string   `json:"waiting"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Report   string   `json:"report"`
}

func (t *Tools) errandStatus(ctx context.Context, conv, _ int64, args string) (any, error) {
	run, err := t.runArg(ctx, conv, args)
	if err != nil {
		return nil, err
	}
	d, found, err := t.errands.AgentRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, refuse("run %d is not kept", run)
	}
	out := statusOut{State: d.State, Options: []string{}, Report: d.Report}
	var q *signins.Question
	if out.Waiting, q = t.errands.Waiting(run); q != nil {
		out.Question = q.Text
		out.Options = append(out.Options, q.Options...)
	}
	return out, nil
}

type appointmentOut struct {
	ID     int64  `json:"id"`
	Site   string `json:"site"`
	Title  string `json:"title"`
	Date   string `json:"date"`
	Time   string `json:"time"`
	Place  string `json:"place"`
	Status string `json:"status"`
}

func (t *Tools) listAppointments(ctx context.Context, _, _ int64, args string) (any, error) {
	if err := parseArgs(args); err != nil {
		return nil, err
	}
	appts, err := t.errands.Appointments(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]appointmentOut, 0, len(appts))
	for _, a := range appts {
		out = append(out, appointmentOut{ID: a.ID, Site: a.Site, Title: a.Title, Date: a.Date, Time: a.Time, Place: a.Place, Status: a.Status})
	}
	slices.SortStableFunc(out, func(a, b appointmentOut) int {
		return boolOrder(a.Status != "upcoming", b.Status != "upcoming")
	})
	return struct {
		Appointments []appointmentOut `json:"appointments"`
	}{out}, nil
}

// boolOrder orders false before true.
func boolOrder(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}

func (t *Tools) checkAppointment(ctx context.Context, conv, _ int64, args string) (any, error) {
	var id int64
	if err := parseArgs(args, integer("id", &id)); err != nil {
		return nil, err
	}
	run, err := t.errands.CheckAppointment(ctx, id)
	if err != nil {
		return nil, err
	}
	return t.linked(ctx, conv, run)
}
