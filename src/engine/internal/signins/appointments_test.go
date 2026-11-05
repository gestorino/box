package signins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"concierge/engine/internal/browser"
)

// arranged is an appointment as the worker reads it out of a run's words.
func arranged(title, date, time string) map[string]any {
	return map[string]any{"title": title, "date": date, "time": time, "place": "Shop St 1",
		"details": []any{map[string]any{"label": "Service", "value": "Haircut"}}}
}

// doneWith is a run_agent that ends done, answering extra besides.
func doneWith(extra browser.Message) func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
	return func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
		reply := browser.Message{"outcome": "done", "success": true, "report": "Done."}
		for k, v := range extra {
			reply[k] = v
		}
		return reply, nil
	}
}

// notDone is a run_agent whose model finished without the action.
func notDone(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
	return browser.Message{"outcome": "done", "success": false, "report": "There is no such thing on the page."}, nil
}

func (f *fixture) start(t *testing.T, e Errand) int64 {
	t.Helper()
	id, err := f.svc.StartErrand(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	return id
}

func (f *fixture) appointments(t *testing.T) []Appointment {
	t.Helper()
	got, err := f.store.Appointments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *fixture) seed(t *testing.T, site, title, date, time, status string) int64 {
	t.Helper()
	id, err := f.store.AddAppointment(context.Background(), site, 7, Arranged{Title: title, Date: date, Time: time,
		Place: "Shop St 1", Details: []Detail{}}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if status != "upcoming" {
		if err := f.store.SetAppointmentStatus(context.Background(), id, status, false, testNow); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestAnErrandThatCreatesKeepsWhatItArranged(t *testing.T) {
	f := errandFixture(t, "registered")
	f.worker.onAgent = doneWith(browser.Message{"appointment": arranged("Haircut with Ana", "2026-11-10", "15:00")})
	run := f.start(t, Errand{URL: "https://shop.test/a", Action: "Get a haircut (Who: Ana)", Effect: "create"})

	if got := f.worker.sent()[0]["record"]; got != "appointment" {
		t.Fatalf("record = %v", got)
	}
	got := f.appointments(t)
	if len(got) != 1 {
		t.Fatalf("appointments = %+v", got)
	}
	a := got[0]
	if a.Site != shopKey || a.Title != "Haircut with Ana" || a.Date != "2026-11-10" || a.Time != "15:00" || a.Place != "Shop St 1" ||
		a.Status != "upcoming" || a.Run != run || !reflect.DeepEqual(a.Details, []Detail{{Label: "Service", Value: "Haircut"}}) {
		t.Fatalf("appointment = %+v", a)
	}
}

func TestOnlyADoneErrandThatCreatesKeepsOne(t *testing.T) {
	f := errandFixture(t, "registered")
	f.worker.onAgent = doneWith(browser.Message{"appointment": arranged("Haircut", "2026-11-10", "15:00")})
	f.start(t, Errand{URL: "https://shop.test/a", Action: "Look around"})
	if got := f.worker.sent()[0]["record"]; got != nil {
		t.Fatalf("an errand with no effect asks for a record: %v", got)
	}
	f.worker.onAgent = notDone
	f.start(t, Errand{URL: "https://shop.test/a", Action: "Get a haircut", Effect: "create"})
	f.worker.onAgent = doneWith(nil) // done, but nothing was read
	f.start(t, Errand{URL: "https://shop.test/a", Action: "Get a haircut", Effect: "create"})
	if got := f.appointments(t); len(got) != 0 {
		t.Fatalf("appointments = %+v", got)
	}
}

func TestRescheduleChangesItsAppointment(t *testing.T) {
	f := errandFixture(t, "registered")
	id := f.seed(t, shopKey, "Haircut", "2026-11-10", "15:00", "upcoming")
	f.worker.onAgent = doneWith(browser.Message{"appointment": arranged("Haircut", "2026-11-12", "10:30")})
	f.start(t, Errand{URL: "https://shop.test/a", Action: "Move a visit (appointment: Haircut, 2026-11-10 15:00)", Effect: "change", Appointment: id})

	if got := f.worker.sent()[0]["record"]; got != "appointment" {
		t.Fatalf("record = %v", got)
	}
	a := f.appointments(t)[0]
	if a.ID != id || a.Date != "2026-11-12" || a.Time != "10:30" || a.Status != "upcoming" || a.Run != 7 {
		t.Fatalf("appointment = %+v", a)
	}
}

func TestARescheduleWithoutAReadingLeavesItAsItWas(t *testing.T) {
	f := errandFixture(t, "registered")
	id := f.seed(t, shopKey, "Haircut", "2026-11-10", "15:00", "upcoming")
	f.worker.onAgent = doneWith(nil)
	f.start(t, Errand{URL: "https://shop.test/a", Action: "Move a visit", Effect: "change", Appointment: id})
	if a := f.appointments(t)[0]; a.Date != "2026-11-10" || a.CheckedAt != nil || !a.UpdatedAt.Equal(testNow) {
		t.Fatalf("appointment = %+v", a)
	}
}

func TestCancelMarksItsAppointmentCancelled(t *testing.T) {
	f := errandFixture(t, "registered")
	id := f.seed(t, shopKey, "Haircut", "2026-11-10", "15:00", "upcoming")
	f.worker.onAgent = notDone
	f.start(t, Errand{URL: "https://shop.test/a", Action: "Call it off", Effect: "cancel", Appointment: id})
	if a := f.appointments(t)[0]; a.Status != "upcoming" {
		t.Fatalf("not done, yet %+v", a)
	}
	f.worker.onAgent = nil // done
	f.start(t, Errand{URL: "https://shop.test/a", Action: "Call it off", Effect: "cancel", Appointment: id})
	if got := f.worker.sent()[1]["record"]; got != nil {
		t.Fatalf("a cancel asks for a record: %v", got)
	}
	if a := f.appointments(t)[0]; a.Status != "cancelled" {
		t.Fatalf("appointment = %+v", a)
	}
}

func TestCheckFindsTheAppointmentOrSaysItIsGone(t *testing.T) {
	f := errandFixture(t, "registered")
	id := f.seed(t, shopKey, "Haircut", "2026-11-10", "15:00", "upcoming")
	check := func(play func(context.Context, browser.Message, browser.Ask) (browser.Message, error)) Appointment {
		t.Helper()
		f.worker.onAgent = play
		if _, err := f.svc.CheckAppointment(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		f.svc.runs.Wait()
		return f.appointments(t)[0]
	}
	if a := check(doneWith(browser.Message{"status": "there"})); a.Status != "upcoming" || a.CheckedAt == nil || !a.CheckedAt.Equal(testNow) {
		t.Fatalf("there: %+v", a)
	}
	body := f.worker.sent()[0]
	if body["action"] != "Check the appointment: Haircut, 2026-11-10 15:00, Shop St 1" || body["url"] != "https://www.shop.test/start" || body["record"] != "status" {
		t.Fatalf("body = %v", body)
	}
	// The check is done (it looked) yet found nothing: the reading decides, not the run's success.
	if a := check(doneWith(browser.Message{"status": "gone"})); a.Status != "not found" {
		t.Fatalf("gone: %+v", a)
	}
	// Not done and the words don't say (signing in failed): nothing changes.
	if a := check(notDone); a.Status != "not found" {
		t.Fatalf("unknown: %+v", a)
	}
	if a := check(func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
		return browser.Message{"outcome": "done", "success": false, "report": "It shows as cancelled.", "status": "cancelled"}, nil
	}); a.Status != "cancelled" {
		t.Fatalf("cancelled: %+v", a)
	}
	if a := check(doneWith(browser.Message{"status": "there"})); a.Status != "upcoming" {
		t.Fatalf("there again: %+v", a)
	}
	if v := f.runDetail(t, int64(len(f.worker.sent()))); v.Kind != "errand" {
		t.Fatalf("run = %+v", v.Run)
	}
}

func TestACheckNeedsTheBoxsAccount(t *testing.T) {
	f := errandFixture(t, "")
	id := f.seed(t, shopKey, "Haircut", "2026-11-10", "15:00", "upcoming")
	if _, err := f.svc.CheckAppointment(context.Background(), id); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("err = %v", err)
	}
	if _, err := f.svc.CheckAppointment(context.Background(), id+1); !errors.Is(err, ErrNoAppointment) {
		t.Fatalf("unknown: err = %v", err)
	}
}

func TestSyncReconcilesTheSitesAppointments(t *testing.T) {
	f := errandFixture(t, "registered")
	kept := f.seed(t, shopKey, "Haircut", "2026-11-10", "15:00", "upcoming")
	gone := f.seed(t, shopKey, "Beard trim", "2026-11-12", "10:00", "upcoming")
	cancelled := f.seed(t, shopKey, "Colour", "2026-11-14", "09:00", "cancelled")
	other := f.seed(t, "other.test", "Lesson", "2026-11-15", "18:00", "upcoming")
	f.worker.onAgent = doneWith(browser.Message{"appointments": []any{
		arranged("Haircut with Ana", "2026-11-10", "15:00"), arranged("Massage", "2026-11-20", "11:00")}})
	run := f.start(t, Errand{URL: "https://shop.test/a", Action: "See my visits", Effect: "sync"})

	if got := f.worker.sent()[0]["record"]; got != "appointments" {
		t.Fatalf("record = %v", got)
	}
	byID := map[int64]Appointment{}
	var added []Appointment
	for _, a := range f.appointments(t) {
		byID[a.ID] = a
		if a.ID > other {
			added = append(added, a)
		}
	}
	if a := byID[kept]; a.Status != "upcoming" || a.Title != "Haircut with Ana" || a.CheckedAt == nil {
		t.Fatalf("kept: %+v", a)
	}
	if a := byID[gone]; a.Status != "not found" {
		t.Fatalf("gone: %+v", a)
	}
	if a := byID[cancelled]; a.Status != "cancelled" {
		t.Fatalf("cancelled: %+v", a)
	}
	if a := byID[other]; a.Status != "upcoming" {
		t.Fatalf("another site's: %+v", a)
	}
	if len(added) != 1 || added[0].Title != "Massage" || added[0].Run != run || added[0].Site != shopKey {
		t.Fatalf("added: %+v", added)
	}
}

func TestASyncMatchesByDayAndNeverRevivesACancelledOne(t *testing.T) {
	f := errandFixture(t, "registered")
	untimed := f.seed(t, shopKey, "Haircut", "2026-11-10", "", "upcoming")
	twinA := f.seed(t, shopKey, "Lesson", "2026-11-12", "10:00", "upcoming")
	twinB := f.seed(t, shopKey, "Lesson", "2026-11-12", "10:00", "upcoming")
	off := f.seed(t, shopKey, "Colour", "2026-11-14", "09:00", "cancelled")
	f.worker.onAgent = doneWith(browser.Message{"appointments": []any{
		arranged("haircut", "2026-11-10", "15:00"), arranged("Lesson", "2026-11-12", "10:00"), arranged("Lesson", "2026-11-12", "10:00"),
		arranged("Colour", "2026-11-14", "09:00")}})
	f.start(t, Errand{URL: "https://shop.test/a", Action: "See my visits", Effect: "sync"})
	got := f.appointments(t)
	byID := map[int64]Appointment{}
	for _, a := range got {
		byID[a.ID] = a
	}
	if len(got) != 4 {
		t.Fatalf("a listed one was added twice: %+v", got)
	}
	if a := byID[untimed]; a.Status != "upcoming" || a.Time != "15:00" || a.CheckedAt == nil {
		t.Fatalf("untimed: %+v", a)
	}
	if byID[twinA].Status != "upcoming" || byID[twinB].Status != "upcoming" {
		t.Fatalf("twins: %+v %+v", byID[twinA], byID[twinB])
	}
	if a := byID[off]; a.Status != "cancelled" {
		t.Fatalf("revived: %+v", a)
	}
}

func TestASyncThatReadNothingMarksNothingGone(t *testing.T) {
	f := errandFixture(t, "registered")
	f.seed(t, shopKey, "Haircut", "2026-11-10", "15:00", "upcoming")
	f.worker.onAgent = doneWith(browser.Message{"appointments": []any{}})
	f.start(t, Errand{URL: "https://shop.test/a", Action: "See my visits", Effect: "sync"})
	if a := f.appointments(t)[0]; a.Status != "upcoming" {
		t.Fatalf("appointment = %+v", a)
	}
}

func TestASyncWithoutTheBoxsAccountMarksNothingGone(t *testing.T) {
	f := errandFixture(t, "")
	id := f.seed(t, shopKey, "Haircut", "2026-11-10", "15:00", "upcoming")
	f.worker.onAgent = doneWith(browser.Message{"appointments": []any{arranged("Lesson", "2026-11-15", "18:00")}})
	f.start(t, Errand{URL: "https://shop.test/a", Action: "See my visits", Effect: "sync"})
	if a := f.appointments(t)[0]; a.ID != id || a.Status != "upcoming" {
		t.Fatalf("appointment = %+v", a)
	}
}

func TestAnErrandsEffectAndAppointmentMustFit(t *testing.T) {
	f := errandFixture(t, "registered")
	elsewhere := f.seed(t, "other.test", "Lesson", "2026-11-15", "18:00", "upcoming")
	for _, e := range []Errand{
		{URL: "https://shop.test/a", Action: "Do it", Effect: "explode"},
		{URL: "https://shop.test/a", Action: "Do it", Effect: "cancel", Appointment: elsewhere},
		{URL: "https://shop.test/a", Action: "Do it", Effect: "cancel", Appointment: 999},
		{URL: "https://shop.test/a", Action: "Do it", Effect: "create", Appointment: elsewhere},
	} {
		if _, err := f.svc.StartErrand(context.Background(), e); !errors.Is(err, ErrBadErrand) {
			t.Fatalf("%+v: err = %v", e, err)
		}
	}
}

func TestAppointmentsOverHTTP(t *testing.T) {
	f := errandFixture(t, "registered")
	h := NewRunsHandler(f.svc)
	later := f.seed(t, shopKey, "Beard trim", "2026-11-12", "10:00", "upcoming")
	f.seed(t, shopKey, "Colour", "2026-11-01", "09:00", "cancelled")
	sooner := f.seed(t, shopKey, "Haircut", "2026-11-10", "15:00", "upcoming")

	r, list := runsList(t, h, "/api/v1/appointments")
	if r != http.StatusOK || len(list) != 3 || list[0]["id"] != float64(sooner) || list[1]["id"] != float64(later) ||
		list[2]["status"] != "cancelled" || list[0]["site"] != shopKey {
		t.Fatalf("list: %d %v", r, list)
	}
	code, body := runsCall(t, h, "POST", fmt.Sprintf("/api/v1/appointments/%d/check", sooner), "")
	if code != http.StatusAccepted || body["run"] == nil {
		t.Fatalf("check: %d %v", code, body)
	}
	f.svc.runs.Wait()
	code, _ = runsCall(t, h, "POST", "/api/v1/errands",
		fmt.Sprintf(`{"url":"https://shop.test/a","action":"Call it off","effect":"cancel","appointment":%d}`, later))
	if code != http.StatusAccepted {
		t.Fatalf("cancel: %d", code)
	}
	f.svc.runs.Wait()
	if code, _ := runsCall(t, h, "DELETE", fmt.Sprintf("/api/v1/appointments/%d", sooner), ""); code != http.StatusNoContent {
		t.Fatalf("forget: %d", code)
	}
	if code, _ := runsCall(t, h, "DELETE", fmt.Sprintf("/api/v1/appointments/%d", sooner), ""); code != http.StatusNotFound {
		t.Fatalf("forget again: %d", code)
	}
	if code, _ := runsCall(t, h, "POST", "/api/v1/appointments/999/check", ""); code != http.StatusNotFound {
		t.Fatalf("check an unknown one: %d", code)
	}
	_, list = runsList(t, h, "/api/v1/appointments")
	if len(list) != 2 || list[0]["status"] != "cancelled" || list[1]["status"] != "cancelled" {
		t.Fatalf("after: %v", list)
	}
}

func TestTheSitesPageListsItsAppointments(t *testing.T) {
	f := newHandlerFixture(t)
	f.seed(t, shopKey, "Haircut", "2026-11-10", "15:00", "upcoming")
	f.seed(t, "other.test", "Lesson", "2026-11-15", "18:00", "upcoming")
	rec := f.do("GET", "/api/v1/accounts/shop.test", "")
	if !strings.Contains(rec.Body.String(), `"appointments":[{"id":1,"site":"shop.test","title":"Haircut"`) || strings.Contains(rec.Body.String(), "Lesson") {
		t.Fatalf("site: %s", rec.Body)
	}
}

func TestRemovingACardForgetsItsAppointments(t *testing.T) {
	f := newFixture(t)
	f.seed(t, shopKey, "Haircut", "2026-11-10", "15:00", "upcoming")
	if err := f.svc.RemoveCard(context.Background(), shopKey); err != nil {
		t.Fatal(err)
	}
	if got := f.appointments(t); len(got) != 0 {
		t.Fatalf("appointments = %+v", got)
	}
}

func runsList(t *testing.T, h http.Handler, path string) (int, []map[string]any) {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %s", path, rec.Body)
	}
	return rec.Code, out
}

func TestAnActionSaysItsEffect(t *testing.T) {
	got, err := cardActions([]Action{{Text: "Get a haircut", Effect: "create"}, {Text: "Look around"}})
	if err != nil || got[0].Effect != "create" || got[1].Effect != "none" {
		t.Fatalf("actions = %+v %v", got, err)
	}
	if _, err := cardActions([]Action{{Text: "Get a haircut", Effect: "explode"}}); err == nil {
		t.Fatal("an unknown effect is kept")
	}
	f := newFixture(t)
	f.worker.onCard = func(browser.Message) (browser.Message, error) {
		a, b := act("Get a haircut"), act("Call it off")
		a["effect"], b["effect"] = "create", "cancel"
		return browser.Message{"name": "New", "actions": []any{a, b}}, nil
	}
	sg, err := f.svc.Suggest(context.Background(), "https://new.test/")
	if err != nil || len(sg.Actions) != 2 || sg.Actions[0].Effect != "create" || sg.Actions[1].Effect != "cancel" {
		t.Fatalf("suggestion = %+v %v", sg, err)
	}
}

func TestV19GivesEachStoredActionAnEffect(t *testing.T) {
	dir, err := os.MkdirTemp("", "si")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "signins.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// v18's sites: actions as {text, params}.
	if _, err := db.Exec(`CREATE TABLE sites (
		key TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, link TEXT NOT NULL,
		icon TEXT NOT NULL, actions TEXT NOT NULL DEFAULT '[]', added_at TEXT NOT NULL);
		INSERT INTO sites VALUES ('shop.test', 'Shop', '', 'https://shop.test/', '', '[{"text":"Order a thing","params":["What"]}]', '2026-11-03T10:00:00Z');
		PRAGMA user_version = 18;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, _, err := s.Card(context.Background(), shopKey)
	if err != nil || !reflect.DeepEqual(c.Actions, []Action{{Text: "Order a thing", Params: []string{"What"}, Effect: "none"}}) {
		t.Fatalf("actions = %+v %v", c.Actions, err)
	}
	var v int
	s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 20 {
		t.Fatalf("v = %d", v)
	}
}

// confirmsThenDone is a run whose model asks the owner Confirm, then ends done.
func confirmsThenDone(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
	ask(ctx, browser.AskRequest{Name: "question", Why: "Which day?", Options: []string{"Wed"}})
	ask(ctx, browser.AskRequest{Name: "question", Why: "Confirm: Haircut, Wed 4 Nov 10:00?", Options: []string{"yes", "no"}})
	return browser.Message{"outcome": "done", "success": true, "report": "Confirmed for Wed 4 Nov at 10:00."}, nil
}

// answered starts an errand that asks twice and answers both.
func (f *fixture) answered(t *testing.T, e Errand) int64 {
	t.Helper()
	f.worker.onAgent = confirmsThenDone
	id, err := f.svc.StartErrand(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		soon(t, "a question waits", func() bool { return f.runDetail(t, id).Question != nil })
		if err := f.svc.Answer(id, "yes"); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.runs.Wait()
	return id
}

func TestADoneErrandKeepsItsAppointmentAfterwards(t *testing.T) {
	f := errandFixture(t, "registered")
	run := f.answered(t, Errand{URL: "https://shop.test/a", Action: "Get a haircut"})
	a, err := f.svc.KeepAppointment(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	ask := f.worker.arrangedAsks[0]
	if ask["action"] != "Get a haircut" || ask["confirm"] != "Confirm: Haircut, Wed 4 Nov 10:00?" || ask["report"] != "Confirmed for Wed 4 Nov at 10:00." {
		t.Fatalf("asked = %v", ask)
	}
	if a.Site != shopKey || a.Run != run || a.Date != "2026-11-04" || a.Status != "upcoming" {
		t.Fatalf("appointment = %+v", a)
	}
	if d := f.runDetail(t, run); d.Appointment != a.ID {
		t.Fatalf("the run's page doesn't point at it: %d", d.Appointment)
	}
	if _, err := f.svc.KeepAppointment(context.Background(), run); !errors.Is(err, ErrAppointmentKept) {
		t.Fatalf("again: err = %v", err)
	}
}

func TestOnlyADoneErrandWhoseWordsNameOneKeepsAnAppointment(t *testing.T) {
	f := errandFixture(t, "registered")
	f.worker.onAgent = notDone
	notDoneRun := f.start(t, Errand{URL: "https://shop.test/a", Action: "Get a haircut"})
	if _, err := f.svc.KeepAppointment(context.Background(), notDoneRun); !errors.Is(err, ErrNotKeepable) {
		t.Fatalf("not done: err = %v", err)
	}
	f.worker.onAgent = nil
	run := f.start(t, Errand{URL: "https://shop.test/a", Action: "Look around"})
	f.worker.onArranged = func(browser.Message) (browser.Message, error) { return browser.Message{}, nil }
	if _, err := f.svc.KeepAppointment(context.Background(), run); !errors.Is(err, ErrNothingArranged) {
		t.Fatalf("nothing: err = %v", err)
	}
	if _, err := f.svc.KeepAppointment(context.Background(), 999); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("unknown: err = %v", err)
	}
	if got := f.appointments(t); len(got) != 0 {
		t.Fatalf("appointments = %+v", got)
	}
}

func TestKeepingAnAppointmentOverHTTP(t *testing.T) {
	f := errandFixture(t, "registered")
	h := NewRunsHandler(f.svc)
	f.worker.onAgent = nil
	run := f.start(t, Errand{URL: "https://shop.test/a", Action: "Get a haircut"})
	code, body := runsCall(t, h, "POST", fmt.Sprintf("/api/v1/runs/%d/appointment", run), "")
	if code != http.StatusCreated || body["title"] != "Haircut" {
		t.Fatalf("keep: %d %v", code, body)
	}
	if code, _ := runsCall(t, h, "POST", fmt.Sprintf("/api/v1/runs/%d/appointment", run), ""); code != http.StatusConflict {
		t.Fatalf("again: %d", code)
	}
	f.worker.onArranged = func(browser.Message) (browser.Message, error) { return browser.Message{}, nil }
	other := f.start(t, Errand{URL: "https://shop.test/a", Action: "Look around"})
	if code, _ := runsCall(t, h, "POST", fmt.Sprintf("/api/v1/runs/%d/appointment", other), ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("nothing arranged: %d", code)
	}
}
