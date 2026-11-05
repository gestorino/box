package signins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"concierge/engine/internal/browser"
)

const (
	footURL = "https://www.shop.test/account/me"
	newURL  = "https://www.shop.test/account/new"
)

// withFootprint stores the box's registered account on shop.test, with the owner's names, and its
// footprint (none when url is ""), with clicks after its link.
func (f *fixture) withFootprint(t *testing.T, url string, clicks ...string) {
	t.Helper()
	ctx := context.Background()
	if err := f.secrets.Put(ctx, "signins/"+shopKey+"/password", testPassword); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutAccount(ctx, Account{Key: shopKey, Email: "owner+shop@example.com",
		Owner: map[string]string{"first_name": "Alex", "last_name": "Sample"}, State: "registered", CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetFootprint(ctx, shopKey, url, clicks); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) footprint(t *testing.T) string {
	t.Helper()
	a, _ := f.account(t)
	return a.Footprint
}

// check runs a Check sign-in to its end and answers the run.
func (f *fixture) check(t *testing.T) AgentRunDetail {
	t.Helper()
	id, err := f.svc.CheckSignIn(context.Background(), shopKey)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	d, _, err := f.svc.AgentRun(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// readsAs answers each read by its URL ("error": the request fails).
func (f *fixture) readsAs(results map[string]string) {
	f.worker.onRead = func(body browser.Message) (browser.Message, error) {
		r := results[body["url"].(string)]
		if r == "error" {
			return nil, errors.New("the worker went away")
		}
		found := ""
		if r == "signed_in" {
			found = "email"
		}
		return browser.Message{"result": r, "found": found, "url": body["url"]}, nil
	}
}

// signsInAt makes the model part end signed in, the worker having proved page and clicks as its
// footprint ("" page: nothing proved).
func (f *fixture) signsInAt(page string, clicks ...string) {
	f.worker.onCheck = func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
		reply := browser.Message{"outcome": "done", "success": true, "report": "signed in", "footprint": nil}
		if page != "" {
			named := []any{} // as the IPC's JSON decodes it
			for _, c := range clicks {
				named = append(named, c)
			}
			reply["footprint"] = map[string]any{"url": page, "clicks": named}
		}
		return reply, nil
	}
}

func TestV15AndV16AddAnEmptyFootprint(t *testing.T) {
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
	// v14's accounts, before the footprint.
	if _, err := db.Exec(`CREATE TABLE accounts (
		key TEXT PRIMARY KEY, email TEXT NOT NULL, password BLOB NOT NULL, owner_json TEXT NOT NULL,
		state TEXT NOT NULL, reason TEXT NOT NULL, run INTEGER NOT NULL, created_at TEXT NOT NULL);
		INSERT INTO accounts VALUES ('shop.test', 'a@b.c', x'00', '{}', 'registered', '', 1, '2026-11-03T10:00:00Z');
		PRAGMA user_version = 14;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, has, err := s.Account(context.Background(), shopKey)
	if err != nil || !has || a.Footprint != "" || a.Email != "a@b.c" {
		t.Fatalf("account = %+v %v %v", a, has, err)
	}
	if a.FootprintClicks == nil || len(a.FootprintClicks) != 0 {
		t.Fatalf("clicks = %#v", a.FootprintClicks)
	}
	var v int
	s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 20 {
		t.Fatalf("v = %d", v)
	}
}

func TestAFootprintCheckSignedInUsesNoModel(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	d := f.check(t)
	if len(f.worker.sent()) != 0 {
		t.Fatal("the model ran")
	}
	if d.Kind != "check" || d.State != "ended" || d.Reason != "signed in" {
		t.Fatalf("check = %+v", d.AgentRunView)
	}
	if d.LLM == nil || d.LLM.Calls != 0 || d.LLM.Partial {
		t.Fatalf("llm = %+v", d.LLM)
	}
	if !strings.Contains(f.runText(t), "box: Read the saved page: signed in (your email)") {
		t.Fatalf("log:\n%s", f.runText(t))
	}
	read := f.worker.read()[0]
	want := map[string]string{"email": "owner+shop@example.com", "first_name": "Alex", "last_name": "Sample",
		"phone": "+351912345678"}
	if read["url"] != footURL || read["session"] != shopKey || !reflect.DeepEqual(read["proofs"], want) {
		t.Fatalf("read = %v", read)
	}
	// The details are looked for, never written down.
	if strings.Contains(f.runText(t), "owner+shop") || strings.Contains(f.runText(t), "912345678") {
		t.Fatalf("a detail reached the log:\n%s", f.runText(t))
	}
}

func TestAFootprintCheckWorksWithoutTheKey(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	f.setKey(t, false)
	if d := f.check(t); d.Reason != "signed in" {
		t.Fatalf("check = %+v", d.AgentRunView)
	}
}

func TestACheckWithoutAFootprintNeedsTheKey(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, "")
	f.setKey(t, false)
	if _, err := f.svc.CheckSignIn(context.Background(), shopKey); !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestSignedOutGoesOnToTheModelAndItsPageBecomesTheFootprint(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	f.readsAs(map[string]string{footURL: "signed_out", newURL: "signed_in"})
	f.signsInAt(newURL)
	d := f.check(t)
	if len(f.worker.sent()) != 1 || d.Reason != "signed in" {
		t.Fatalf("errands = %d, check = %+v", len(f.worker.sent()), d.AgentRunView)
	}
	if f.footprint(t) != newURL {
		t.Fatalf("footprint = %q", f.footprint(t))
	}
	// The worker proves in the model's own browser, the old footprint first: the engine reads nothing after.
	prove, _ := f.worker.sent()[0]["prove"].(browser.Message)
	if old, _ := prove["old"].(map[string]any); old["url"] != footURL || len(f.worker.read()) != 1 {
		t.Fatalf("prove = %v, reads = %d", prove, len(f.worker.read()))
	}
	if proofs, _ := prove["proofs"].(map[string]string); proofs["email"] != "owner+shop@example.com" {
		t.Fatalf("proofs = %v", prove["proofs"])
	}
	log := f.runText(t)
	for _, line := range []string{"box: Read the saved page: signed out: the site asks to sign in",
		"box: Kept this page as the sign-in footprint"} {
		if !strings.Contains(log, line) {
			t.Fatalf("no %q in:\n%s", line, log)
		}
	}
}

func TestABrokenFootprintGoesOnToTheModelToo(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	f.readsAs(map[string]string{footURL: "broken", newURL: "signed_in"})
	f.signsInAt(newURL)
	f.check(t)
	if len(f.worker.sent()) != 1 || f.footprint(t) != newURL {
		t.Fatalf("errands = %d, footprint = %q", len(f.worker.sent()), f.footprint(t))
	}
	if !strings.Contains(f.runText(t), "box: Read the saved page: it shows neither") {
		t.Fatalf("log:\n%s", f.runText(t))
	}
}

func TestTheFirstSignInLeavesTheFirstFootprint(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, "")
	f.signsInAt(newURL)
	f.check(t)
	if f.footprint(t) != newURL {
		t.Fatalf("footprint = %q", f.footprint(t))
	}
}

func TestAModelPartWithoutAFinalPageLeavesTheFootprint(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	f.readsAs(map[string]string{footURL: "signed_out"})
	f.worker.onCheck = func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
		return browser.Message{"outcome": "failed", "success": false, "error": "the browser went away"}, nil
	}
	f.check(t)
	if f.footprint(t) != footURL || len(f.worker.read()) != 1 {
		t.Fatalf("footprint = %q, reads = %d", f.footprint(t), len(f.worker.read()))
	}
}

func TestV16AddsNoClicks(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	if a, _ := f.account(t); a.FootprintClicks == nil || len(a.FootprintClicks) != 0 {
		t.Fatalf("clicks = %#v", a.FootprintClicks)
	}
}

func TestASignInThatProvedNothingClearsTheFootprint(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	f.readsAs(map[string]string{footURL: "broken"})
	f.signsInAt("")
	f.check(t)
	if f.footprint(t) != "" {
		t.Fatalf("footprint = %q", f.footprint(t))
	}
	if !strings.Contains(f.runText(t), "box: No page the box could open again showed the account: no footprint kept") {
		t.Fatalf("log:\n%s", f.runText(t))
	}
}

func TestAHumanCheckOnTheSavedPageGoesOnToTheModel(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	f.readsAs(map[string]string{footURL: "human_check"})
	f.signsInAt(footURL)
	f.check(t)
	if len(f.worker.sent()) != 1 || !strings.Contains(f.runText(t), "box: Read the saved page: a human check is in the way") {
		t.Fatalf("errands = %d, log:\n%s", len(f.worker.sent()), f.runText(t))
	}
}

func TestTheSavedClicksAreRepeatedAndOnlyCounted(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL, "Alex Menu", "Details")
	f.check(t)
	if clicks, _ := f.worker.read()[0]["clicks"].([]string); !reflect.DeepEqual(clicks, []string{"Alex Menu", "Details"}) {
		t.Fatalf("read = %v", f.worker.read()[0])
	}
	log := f.runText(t)
	if !strings.Contains(log, "box: Reading the saved page /account/me after 2 clicks\n") || strings.Contains(log, "Details") {
		t.Fatalf("log:\n%s", log)
	}
	v, _, _, _ := f.svc.Site(context.Background(), shopKey)
	if v.Account.FootprintClicks != 2 {
		t.Fatalf("account = %+v", v.Account)
	}
}

func TestAProvedFootprintKeepsItsClicks(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, "")
	f.signsInAt("https://www.shop.test/", "Alex Menu", "Details")
	f.check(t)
	a, _ := f.account(t)
	if a.Footprint != "https://www.shop.test/" || !reflect.DeepEqual(a.FootprintClicks, []string{"Alex Menu", "Details"}) {
		t.Fatalf("account = %q %v", a.Footprint, a.FootprintClicks)
	}
	if !strings.Contains(f.runText(t), "box: Kept this page as the sign-in footprint, after 2 clicks") {
		t.Fatalf("log:\n%s", f.runText(t))
	}
}

func TestASuccessfulRegistrationStartsASignInCheck(t *testing.T) {
	f := newFixture(t)
	f.signsInAt(newURL)
	reg := f.register(t)
	runs, err := f.store.Runs(context.Background(), shopKey)
	if err != nil || len(runs) != 2 || runs[0].Kind != "check" || runs[1].ID != reg {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	if sent := f.worker.sent(); len(sent) != 2 || sent[1]["action"] != checkAction {
		t.Fatalf("sent = %v", sent)
	}
	// The registration leaves no footprint itself: the sign-in after it does.
	if f.footprint(t) != newURL || len(f.worker.read()) != 0 {
		t.Fatalf("footprint = %q, reads = %d", f.footprint(t), len(f.worker.read()))
	}
}

func TestAFailedRegistrationStartsNoCheck(t *testing.T) {
	f := newFixture(t)
	f.worker.onAgent = func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
		return browser.Message{"outcome": "done", "success": false, "report": "the site said no"}, nil
	}
	f.register(t)
	if runs, _ := f.store.Runs(context.Background(), shopKey); len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestRegisterAgainStartsWithoutAFootprint(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	a, _ := f.account(t)
	a.State, a.Reason = "warn", "the registration was interrupted"
	if err := f.store.PutAccount(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if f.footprint(t) != footURL {
		t.Fatal("PutAccount of the same account lost its footprint")
	}
	f.register(t) // its sign-in ends on no page: nothing to keep
	if f.footprint(t) != "" {
		t.Fatalf("footprint = %q", f.footprint(t))
	}
}

func TestTheSitesPageSaysWhetherChecksAreFree(t *testing.T) {
	f := newHandlerFixture(t)
	f.withFootprint(t, footURL)
	rec := f.do("GET", "/api/v1/accounts/shop.test", "")
	if !strings.Contains(rec.Body.String(), `"footprint":"`+footURL+`"`) {
		t.Fatalf("site: %s", rec.Body)
	}
}

func TestAFreeReadThatFailsEndsTheRunWithoutTheModel(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	f.worker.onRead = func(browser.Message) (browser.Message, error) { return nil, browser.ErrUnavailable }
	d := f.check(t)
	if len(f.worker.sent()) != 0 {
		t.Fatal("the model ran after a read that failed")
	}
	if d.State != "ended" || d.Reason != "the browser worker was away" || d.LLM == nil || d.LLM.Partial || d.LLM.Calls != 0 {
		t.Fatalf("check = %+v %+v", d.AgentRunView, d.LLM)
	}
	if strings.Contains(f.runText(t), "it shows neither") {
		t.Fatalf("log:\n%s", f.runText(t))
	}
}

func TestTheFootprintKeepsNoQueryOrFragment(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, "")
	f.signsInAt(newURL + "?token=s3cr3t#access_token=s3cr3t")
	f.check(t)
	if f.footprint(t) != newURL {
		t.Fatalf("footprint = %q", f.footprint(t))
	}
	if strings.Contains(f.runText(t), "s3cr3t") {
		t.Fatalf("log:\n%s", f.runText(t))
	}
}

func TestACheckLogsOnlyTheSavedPagesPath(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	f.check(t)
	if log := f.runText(t); !strings.Contains(log, "box: Reading the saved page /account/me\n") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestAFreeCheckServedNoModelAsAnEmptyList(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	d := f.check(t)
	// The run page lists what served the run: an empty list, never null.
	if raw, _ := json.Marshal(d); !strings.Contains(string(raw), `"served":[]`) {
		t.Fatalf("detail = %s", raw)
	}
}

func TestARunStoredWithNullServedReadsAsAnEmptyList(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, footURL)
	d := f.check(t)
	if _, err := f.store.db.Exec(`UPDATE agent_runs SET served_json = 'null' WHERE run = ?`, d.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := f.svc.AgentRun(context.Background(), d.ID); got.Served == nil {
		t.Fatal("served is nil")
	}
}

func TestEachProvingTryIsInTheRunLogByItsClickCount(t *testing.T) {
	f := newFixture(t)
	f.withFootprint(t, "")
	f.worker.progress = []browser.Message{
		{"event": "prove", "payload": map[string]any{"clicks": float64(0), "result": "broken"}},
		{"event": "prove", "payload": map[string]any{"clicks": float64(1), "result": "error", "error": "TimeoutError"}},
		{"event": "prove", "payload": map[string]any{"clicks": float64(2), "result": "signed_in"}},
	}
	f.signsInAt(newURL, "Menu", "Details")
	f.check(t)
	log := f.runText(t)
	for _, line := range []string{
		"box: Tried the page it ended on without the model: it shows neither",
		"box: Tried it after 1 click without the model: it could not be opened (TimeoutError)",
		"box: Tried it after 2 clicks without the model: it shows the account",
	} {
		if !strings.Contains(log, line) {
			t.Fatalf("no %q in:\n%s", line, log)
		}
	}
}
