package signins

import (
	"context"
	"fmt"
	"testing"
	"time"

	"concierge/engine/internal/browser"
)

var runT0 = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

func TestARunKeepsItsLinesInOrder(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	id, err := s.StartRun(ctx, shopKey, "errand", runT0)
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range []RunLine{{Kind: "box", Text: "Registering"}, {Kind: "goal", Text: "Click Entrar"}, {Kind: "action", Text: "click “Entrar” — ok"}} {
		if err := s.AddLine(ctx, id, runT0.Add(time.Duration(i)*time.Second), l.Kind, l.Text); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EndRun(ctx, id, runT0.Add(time.Minute), "warn", "a captcha appeared on step 3"); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs(ctx, shopKey)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v %v", runs, err)
	}
	r := runs[0]
	if r.ID != id || r.Kind != "errand" || r.State != "warn" || r.Reason != "a captcha appeared on step 3" ||
		!r.StartedAt.Equal(runT0) || r.EndedAt == nil || !r.EndedAt.Equal(runT0.Add(time.Minute)) {
		t.Fatalf("run = %+v", r)
	}
	got, lines, found, err := s.Run(ctx, shopKey, id)
	if err != nil || !found || got.ID != id {
		t.Fatalf("run = %+v %v %v", got, found, err)
	}
	if len(lines) != 3 || lines[0].Text != "Registering" || lines[1].Kind != "goal" || lines[2].Text != "click “Entrar” — ok" ||
		!lines[2].At.Equal(runT0.Add(2*time.Second)) {
		t.Fatalf("lines = %+v", lines)
	}
	// A run is found only under its own site.
	if _, _, found, _ := s.Run(ctx, "other.test", id); found {
		t.Fatal("found under another site")
	}
}

func TestOnlyTheNewestRunsOfASiteAreKept(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	var ids []int64
	for i := range keptRuns + 2 {
		id, err := s.StartRun(ctx, shopKey, "errand", runT0.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		s.AddLine(ctx, id, runT0, "box", fmt.Sprint("line of ", i))
		ids = append(ids, id)
	}
	other, _ := s.StartRun(ctx, "other.test", "errand", runT0)
	runs, _ := s.Runs(ctx, shopKey)
	if len(runs) != keptRuns || runs[0].ID != ids[len(ids)-1] || runs[keptRuns-1].ID != ids[2] {
		t.Fatalf("kept %d, newest %d, oldest %d", len(runs), runs[0].ID, runs[keptRuns-1].ID)
	}
	var orphans int
	s.db.QueryRow(`SELECT COUNT(*) FROM run_lines WHERE run IN (?, ?)`, ids[0], ids[1]).Scan(&orphans)
	if orphans != 0 {
		t.Fatalf("%d lines of dropped runs remain", orphans)
	}
	if _, _, found, _ := s.Run(ctx, "other.test", other); !found {
		t.Fatal("another site's run was dropped")
	}
}

// A run still running when the store opens belonged to an engine that stopped.
func TestReopeningEndsTheRunsAStoppedEngineLeft(t *testing.T) {
	ctx := context.Background()
	s, path := openTestStore(t)
	id, _ := s.StartRun(ctx, shopKey, "errand", runT0)
	s.Close()
	again, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	r, _, _, err := again.Run(ctx, shopKey, id)
	if err != nil || r.State != "ended" || r.Reason != "the engine restarted" || r.EndedAt == nil {
		t.Fatalf("run = %+v %v", r, err)
	}
}

// lineTexts is a run's lines as "kind: text".
func lineTexts(t *testing.T, s *Store, site string, id int64) (Run, []string) {
	t.Helper()
	r, lines, found, err := s.Run(context.Background(), site, id)
	if err != nil || !found {
		t.Fatalf("run %d: %v %v", id, found, err)
	}
	var out []string
	for _, l := range lines {
		out = append(out, l.Kind+": "+l.Text)
	}
	return r, out
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestARunFromBeforeModelUseWasCountedSaysNothing(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	id, err := s.StartRun(ctx, shopKey, "errand", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM run_usage WHERE run = ?`, id); err != nil {
		t.Fatal(err)
	}
	r, _, _, err := s.Run(ctx, shopKey, id)
	if err != nil || r.LLM != nil {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestDroppingOldRunsDeletesTheirModelUse(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	for range keptRuns + 2 {
		if _, err := s.StartRun(ctx, shopKey, "errand", testNow); err != nil {
			t.Fatal(err)
		}
	}
	count := func() (n int) {
		s.db.QueryRow(`SELECT count(*) FROM run_usage`).Scan(&n)
		return n
	}
	if n := count(); n != keptRuns {
		t.Fatalf("kept %d", n)
	}
}

// Every call goes through OpenRouter, which bills it; one it didn't bill has no price, never $0.
func TestOnlyWhatOpenRouterBilledIsPriced(t *testing.T) {
	u := RunLLM{Priced: true}
	u.add(map[string]any{"model": "m", "llm_calls": float64(1), "billed_usd": 0.002})
	if !u.Priced || u.CostUSD != 0.002 || u.Calls != 1 || u.Model != "m" {
		t.Fatalf("billed = %+v", u)
	}
	u.add(map[string]any{"model": "n", "llm_calls": float64(1)})
	if u.Priced || u.Model != "m, n" || u.Calls != 2 {
		t.Fatalf("unbilled = %+v", u)
	}
}

func TestTheEmailsLinkOrCodeIsSaidInTheConversation(t *testing.T) {
	f := newRecordingFixture(t)
	log, _ := f.open(t)
	log.progress("email_link", browser.Message{"n": float64(3), "words": "Confirm your email"})
	log.progress("email_link", browser.Message{"n": float64(1), "words": ""})
	log.progress("email_code", browser.Message{"n": float64(1)})
	_, lines := lineTexts(t, f.store, shopKey, log.id)
	for _, want := range []string{
		"box: Opened the email's link “Confirm your email”",
		"box: Opened the email's link 1",
		"box: Typed the code from the email",
	} {
		if !contains(lines, want) {
			t.Fatalf("no %q in %q", want, lines)
		}
	}
}

func TestRunning(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	a, _ := s.StartRun(ctx, shopKey, "errand", runT0)
	b, _ := s.StartRun(ctx, shopKey, "check", runT0.Add(time.Hour))
	c, _ := s.StartRun(ctx, "other.test", "errand", runT0.Add(2*time.Hour))
	if err := s.EndRun(ctx, b, runT0.Add(3*time.Hour), "signed_in", ""); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Running(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != a || runs[1].ID != c {
		t.Fatalf("running = %+v", runs)
	}
	if runs[0].Kind != "errand" || runs[0].Site != shopKey || runs[1].Site != "other.test" {
		t.Fatalf("kind/site = %+v", runs)
	}
}

func TestRunsByID(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	a, _ := s.StartRun(ctx, shopKey, "errand", runT0)
	b, _ := s.StartRun(ctx, "other.test", "check", runT0)
	got, err := s.RunsByID(ctx, []int64{a, b, 999})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[a].Kind != "errand" || got[b].Site != "other.test" {
		t.Fatalf("got %+v", got)
	}
	if _, ok := got[999]; ok {
		t.Fatal("a pruned id is present")
	}
	none, err := s.RunsByID(ctx, nil)
	if err != nil || len(none) != 0 {
		t.Fatalf("empty ids: %v %v", none, err)
	}
}
