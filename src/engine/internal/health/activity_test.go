package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"concierge/engine/internal/signins"
)

type fakeRuns struct {
	runs []signins.Run
	err  error
}

func (f fakeRuns) Running(context.Context) ([]signins.Run, error) { return f.runs, f.err }

func get(t *testing.T, l RunLister) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	ActivityHandler(l).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Code, rec.Body.String()
}

func TestActivityHandler(t *testing.T) {
	t.Run("one series per running run", func(t *testing.T) {
		code, body := get(t, fakeRuns{runs: []signins.Run{
			{ID: 7, Site: "s1", Kind: "errand", State: "running"},
			{ID: 8, Site: "s2", Kind: "check", State: "running"},
		}})
		if code != 200 {
			t.Fatalf("status %d", code)
		}
		for _, want := range []string{
			`concierge_run_active{kind="errand",run="7",site="s1"} 1`,
			`concierge_run_active{kind="check",run="8",site="s2"} 1`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %s in:\n%s", want, body)
			}
		}
	})
	t.Run("no runs, no samples", func(t *testing.T) {
		code, body := get(t, fakeRuns{runs: []signins.Run{}})
		if code != 200 {
			t.Fatalf("status %d", code)
		}
		for _, l := range strings.Split(body, "\n") {
			if strings.HasPrefix(l, "concierge_run_active") {
				t.Errorf("unexpected sample %q", l)
			}
		}
	})
	t.Run("lister error is 500", func(t *testing.T) {
		if code, _ := get(t, fakeRuns{err: errors.New("db gone")}); code != 500 {
			t.Fatalf("status %d, want 500", code)
		}
	})
}
