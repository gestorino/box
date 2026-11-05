package signins

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"concierge/engine/internal/browser"
)

// An appointment is what an errand arranged on a site: made by a done errand whose action creates
// one, read by the worker out of the run's own words (appointments.py); changed, cancelled or synced
// by the site's actions with those effects, and checked by an errand of its own. Nothing runs on a
// schedule. Design: docs/superpowers/specs/2026-11-03-appointments-design.md.

// ErrNoAppointment is an appointment the box doesn't keep.
var ErrNoAppointment = errors.New("no such appointment")

// ErrNotKeepable refuses keeping an appointment of a run that isn't a done errand.
var ErrNotKeepable = errors.New("only a done errand keeps what it arranged")

// ErrAppointmentKept refuses keeping a run's appointment twice.
var ErrAppointmentKept = errors.New("this run's appointment is kept already")

// ErrNothingArranged is a done errand whose words name nothing arranged.
var ErrNothingArranged = errors.New("the run's words name nothing it arranged")

// readWait bounds reading a finished run's words: one model call.
const readWait = 45 * time.Second

// Detail is one fact of an appointment: what it is for, who, how much.
type Detail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Arranged is an appointment's own fields, as the worker reads them.
type Arranged struct {
	Title, Date, Time, Place string
	Details                  []Detail
}

// Appointment is one kept: Date is YYYY-MM-DD and Time HH:MM, each "" when the site's words didn't
// say; Status is upcoming, cancelled or "not found" (the site no longer shows it); Run made it;
// CheckedAt is when the site last showed it, nil never.
type Appointment struct {
	ID        int64      `json:"id"`
	Site      string     `json:"site"`
	Title     string     `json:"title"`
	Date      string     `json:"date"`
	Time      string     `json:"time"`
	Place     string     `json:"place"`
	Details   []Detail   `json:"details"`
	Status    string     `json:"status"`
	Run       int64      `json:"run"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

const (
	upcoming  = "upcoming"
	cancelled = "cancelled"
	notFound  = "not found"
)

var (
	dateForm = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	timeForm = regexp.MustCompile(`^\d{2}:\d{2}$`)
)

// arrangedOf is the worker's reading of one appointment; false without a title.
func arrangedOf(v any) (Arranged, bool) {
	m, _ := v.(map[string]any)
	field := func(k string, most int) string { s, _ := m[k].(string); return cut(oneLine(s), most) }
	a := Arranged{Title: field("title", 120), Date: field("date", 10), Time: field("time", 5), Place: field("place", 200),
		Details: []Detail{}}
	if !dateForm.MatchString(a.Date) {
		a.Date = ""
	}
	if !timeForm.MatchString(a.Time) {
		a.Time = ""
	}
	ds, _ := m["details"].([]any)
	for _, d := range ds {
		dm, _ := d.(map[string]any)
		label, _ := dm["label"].(string)
		value, _ := dm["value"].(string)
		if label, value = cut(oneLine(label), 40), cut(oneLine(value), 120); label != "" && value != "" && len(a.Details) < 8 {
			a.Details = append(a.Details, Detail{Label: label, Value: value})
		}
	}
	return a, a.Title != ""
}

// said is how an appointment is named in an errand's words: "Haircut, 2026-11-10 15:00, Shop St 1".
func said(a Appointment, withPlace bool) string {
	parts := []string{a.Title}
	if when := strings.TrimSpace(a.Date + " " + a.Time); when != "" {
		parts = append(parts, when)
	}
	if withPlace && a.Place != "" {
		parts = append(parts, a.Place)
	}
	return strings.Join(parts, ", ")
}

// Appointments is the site's appointments ("" every site's): upcoming first, soonest first.
func (s *Service) Appointments(ctx context.Context, site string) ([]Appointment, error) {
	return s.cfg.Store.Appointments(ctx, site)
}

// CheckAppointment starts an errand on the appointment's site, in the box's account there, that looks
// for it: done, it is still upcoming; not done, it is not found.
func (s *Service) CheckAppointment(ctx context.Context, id int64) (int64, error) {
	a, found, err := s.cfg.Store.Appointment(ctx, id)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrNoAppointment
	}
	if acc, has, err := s.cfg.Store.Account(ctx, a.Site); err != nil {
		return 0, err
	} else if !has || acc.State != "registered" {
		return 0, ErrNoAccount
	}
	link := "https://" + a.Site + "/"
	if c, has, err := s.cfg.Store.Card(ctx, a.Site); err != nil {
		return 0, err
	} else if has {
		link = c.Link
	}
	return s.errand(ctx, Errand{URL: link, Action: "Check the appointment: " + said(a, true), Appointment: id}, "check")
}

// KeepAppointment keeps what a done errand arranged when its action didn't (it had no effect, or ran
// before appointments): the worker reads it out of the run's words, its action, the last Confirm the
// owner answered and its report, as a creating errand's end does.
func (s *Service) KeepAppointment(ctx context.Context, run int64) (Appointment, error) {
	view, report, _, found, err := s.cfg.Store.AgentRun(ctx, run)
	if err != nil {
		return Appointment{}, err
	}
	if !found {
		return Appointment{}, ErrUnknownSite
	}
	if view.Kind != "errand" || view.State != "ended" || view.Reason != "done" {
		return Appointment{}, ErrNotKeepable
	}
	if id, err := s.cfg.Store.AppointmentOfRun(ctx, run); err != nil {
		return Appointment{}, err
	} else if id != 0 {
		return Appointment{}, ErrAppointmentKept
	}
	if err := s.keyed(ctx); err != nil {
		return Appointment{}, err
	}
	_, lines, _, err := s.cfg.Store.Run(ctx, view.Site, run)
	if err != nil {
		return Appointment{}, err
	}
	confirm := ""
	for _, l := range lines {
		if l.Kind == "question" && strings.HasPrefix(l.Text, "Confirm:") {
			confirm = l.Text
		}
	}
	read, cancel := context.WithTimeout(ctx, readWait)
	defer cancel()
	reply, err := s.cfg.Worker.RequestAsk(read, "read_arranged", browser.Message{"action": view.Action, "confirm": confirm,
		"report": report}, nil, nil)
	if err != nil {
		return Appointment{}, err
	}
	a, ok := arrangedOf(reply["appointment"])
	if !ok {
		return Appointment{}, ErrNothingArranged
	}
	id, err := s.cfg.Store.AddAppointment(ctx, view.Site, run, a, s.cfg.Now())
	if err != nil {
		return Appointment{}, err
	}
	kept, _, err := s.cfg.Store.Appointment(ctx, id)
	return kept, err
}

// ForgetAppointment removes the box's record of an appointment; the site keeps it.
func (s *Service) ForgetAppointment(ctx context.Context, id int64) error {
	gone, err := s.cfg.Store.DeleteAppointment(ctx, id)
	if err == nil && !gone {
		err = ErrNoAppointment
	}
	return err
}

// recordOf is what a run of the effect asks the worker to read when it is done.
func recordOf(effect string) string {
	switch effect {
	case "create", "change":
		return "appointment"
	case "sync":
		return "appointments"
	case "check":
		return "status"
	}
	return ""
}

// applyEffect is what an errand's end does to the site's appointments: only a run that finished (not
// stopped, cut short or failed), by the effect of its action and what the worker read of its words. inAccount is whether it ran in the
// box's account there: only then may a site that shows nothing mean an appointment is gone.
func (s *Service) applyEffect(ctx context.Context, site string, run int64, effect string, id int64, inAccount bool, reply browser.Message) {
	done := reply["success"] == true
	now := s.cfg.Now()
	var err error
	switch effect {
	case "create":
		if a, ok := arrangedOf(reply["appointment"]); done && ok {
			_, err = s.cfg.Store.AddAppointment(ctx, site, run, a, now)
		}
	case "change":
		if !done || id == 0 {
			return
		}
		// Without a reading the box doesn't know what it is now: it stays as it was.
		if a, ok := arrangedOf(reply["appointment"]); ok {
			err = s.cfg.Store.ChangeAppointment(ctx, id, a, now)
		}
	case "cancel":
		if done && id != 0 {
			err = s.cfg.Store.SetAppointmentStatus(ctx, id, cancelled, false, now)
		}
	case "check":
		// What the words say the site shows decides, done or not: a check that looked and found
		// nothing may call itself done, and one that couldn't sign in says nothing (no status).
		switch reply["status"] {
		case "there":
			err = s.cfg.Store.SetAppointmentStatus(ctx, id, upcoming, true, now)
		case "cancelled":
			err = s.cfg.Store.SetAppointmentStatus(ctx, id, cancelled, true, now)
		case "gone":
			err = s.cfg.Store.SetAppointmentStatus(ctx, id, notFound, false, now)
		}
	case "sync":
		raw, listed := reply["appointments"].([]any)
		if !done || !listed {
			return
		}
		read := []Arranged{}
		for _, v := range raw {
			if a, ok := arrangedOf(v); ok {
				read = append(read, a)
			}
		}
		err = s.cfg.Store.SyncAppointments(ctx, site, run, read, inAccount, now)
	}
	if err != nil {
		slog.Error("signins: keeping what an errand arranged", "run", run, "effect", effect, "err", err)
	}
}

const appointmentCols = `id, site, title, date, time, place, details_json, status, run, created_at, updated_at, checked_at`

func scanAppointment(r scanner) (Appointment, error) {
	var a Appointment
	var details, created, updated, checked string
	if err := r.Scan(&a.ID, &a.Site, &a.Title, &a.Date, &a.Time, &a.Place, &details, &a.Status, &a.Run, &created, &updated, &checked); err != nil {
		return Appointment{}, err
	}
	if err := json.Unmarshal([]byte(details), &a.Details); err != nil {
		return Appointment{}, err
	}
	var err error
	if a.CreatedAt, err = parseTS(created); err != nil {
		return Appointment{}, err
	}
	if a.UpdatedAt, err = parseTS(updated); err != nil {
		return Appointment{}, err
	}
	if checked != "" {
		at, err := parseTS(checked)
		if err != nil {
			return Appointment{}, err
		}
		a.CheckedAt = &at
	}
	return a, nil
}

func detailsJSON(d []Detail) (string, error) {
	if d == nil {
		d = []Detail{}
	}
	raw, err := json.Marshal(d)
	return string(raw), err
}

// AddAppointment keeps a new upcoming appointment on the site, made by run.
func (s *Store) AddAppointment(ctx context.Context, site string, run int64, a Arranged, at time.Time) (int64, error) {
	return addAppointment(ctx, s.db, site, run, a, at)
}

func addAppointment(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, site string, run int64, a Arranged, at time.Time) (int64, error) {
	details, err := detailsJSON(a.Details)
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `INSERT INTO appointments (site, title, date, time, place, details_json, status, run,
		created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, site, a.Title, a.Date, a.Time, a.Place, details, upcoming,
		run, ts(at), ts(at))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Appointments is the site's appointments ("" every site's), upcoming first, then by date and time.
func (s *Store) Appointments(ctx context.Context, site string) ([]Appointment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+appointmentCols+` FROM appointments WHERE ? = '' OR site = ?
		ORDER BY status != 'upcoming', date = '', date, time, id`, site, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Appointment{}
	for rows.Next() {
		a, err := scanAppointment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Appointment(ctx context.Context, id int64) (Appointment, bool, error) {
	a, err := scanAppointment(s.db.QueryRowContext(ctx, `SELECT `+appointmentCols+` FROM appointments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Appointment{}, false, nil
	}
	return a, err == nil, err
}

// ChangeAppointment writes an appointment's new fields, upcoming and seen on the site at at.
func (s *Store) ChangeAppointment(ctx context.Context, id int64, a Arranged, at time.Time) error {
	details, err := detailsJSON(a.Details)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE appointments SET title = ?, date = ?, time = ?, place = ?, details_json = ?,
		status = ?, updated_at = ?, checked_at = ? WHERE id = ?`, a.Title, a.Date, a.Time, a.Place, details, upcoming, ts(at), ts(at), id)
	return err
}

// SetAppointmentStatus sets an appointment's status; checked, it was seen on the site at at.
func (s *Store) SetAppointmentStatus(ctx context.Context, id int64, status string, checked bool, at time.Time) error {
	q := `UPDATE appointments SET status = ?, updated_at = ? WHERE id = ?`
	args := []any{status, ts(at), id}
	if checked {
		q = `UPDATE appointments SET status = ?, updated_at = ?, checked_at = ? WHERE id = ?`
		args = []any{status, ts(at), ts(at), id}
	}
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// AppointmentOfRun is the appointment the run made; 0 when none.
func (s *Store) AppointmentOfRun(ctx context.Context, run int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM appointments WHERE run = ? ORDER BY id LIMIT 1`, run).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (s *Store) DeleteAppointment(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM appointments WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// keptRow is a kept appointment as a sync matches it.
type keptRow struct {
	id                int64
	title, date, time string
	status            string
	matched           bool
}

// matchOf is the kept row a listed appointment is, first by its day and time, then its day and
// title, then its title where either has no day; nil when none. A row matches once.
func matchOf(rows []*keptRow, a Arranged) *keptRow {
	title := strings.ToLower(a.Title)
	for _, same := range []func(r *keptRow) bool{
		func(r *keptRow) bool { return a.Date != "" && a.Time != "" && r.date == a.Date && r.time == a.Time },
		func(r *keptRow) bool { return a.Date != "" && r.date == a.Date && strings.ToLower(r.title) == title },
		func(r *keptRow) bool { return (a.Date == "" || r.date == "") && strings.ToLower(r.title) == title },
	} {
		for _, r := range rows {
			if !r.matched && same(r) {
				return r
			}
		}
	}
	return nil
}

// SyncAppointments makes the site's kept appointments what its page listed as upcoming: each listed
// one is matched (matchOf) and refreshed, seen at at, or added as made by run; a cancelled one stays
// cancelled. With markGone and a list read, each upcoming one the page didn't list is not found; an
// empty reading marks nothing (the words may have counted them without naming them).
func (s *Store) SyncAppointments(ctx context.Context, site string, run int64, listed []Arranged, markGone bool, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, title, date, time, status FROM appointments WHERE site = ? ORDER BY id`, site)
	if err != nil {
		return err
	}
	var kept []*keptRow
	for rows.Next() {
		r := &keptRow{}
		if err := rows.Scan(&r.id, &r.title, &r.date, &r.time, &r.status); err != nil {
			rows.Close()
			return err
		}
		kept = append(kept, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range listed {
		r := matchOf(kept, a)
		if r == nil {
			if _, err := addAppointment(ctx, tx, site, run, a, at); err != nil {
				return err
			}
			continue
		}
		r.matched = true
		if r.status == cancelled {
			continue
		}
		details, err := detailsJSON(a.Details)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE appointments SET title = ?, date = ?, time = ?, place = ?, details_json = ?,
			status = ?, updated_at = ?, checked_at = ? WHERE id = ?`, a.Title, cmp.Or(a.Date, r.date), cmp.Or(a.Time, r.time),
			a.Place, details, upcoming, ts(at), ts(at), r.id); err != nil {
			return err
		}
	}
	if markGone && len(listed) > 0 {
		for _, r := range kept {
			if r.matched || r.status != upcoming {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE appointments SET status = ?, updated_at = ? WHERE id = ?`, notFound, ts(at), r.id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// errandEffect is an errand's effect checked against the appointment it names: a change, cancel or
// check names one of the site's own; nothing else names one.
func (s *Service) errandEffect(ctx context.Context, site, effect string, id int64) error {
	if id == 0 {
		return nil
	}
	if effect != "change" && effect != "cancel" && effect != "check" {
		return fmt.Errorf("%w: only a change or a cancel names an appointment", ErrBadErrand)
	}
	a, found, err := s.cfg.Store.Appointment(ctx, id)
	if err != nil {
		return err
	}
	if !found || a.Site != site {
		return fmt.Errorf("%w: the appointment is not this site's", ErrBadErrand)
	}
	return nil
}
