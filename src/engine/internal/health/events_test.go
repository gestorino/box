package health

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"concierge/engine/internal/h2c"
	"concierge/engine/internal/signins"
)

const evTo = 1700100000

var evFrom, evEnd = time.Unix(evTo-3600, 0), time.Unix(evTo, 0)

type fakeRunStore struct {
	runs map[int64]signins.Run
	err  error
}

func (f fakeRunStore) RunsByID(_ context.Context, ids []int64) (map[int64]signins.Run, error) {
	return f.runs, f.err
}

type fakeCallLister struct {
	calls []CallInfo
	err   error
}

func (f fakeCallLister) Calls(context.Context) ([]CallInfo, error) { return f.calls, f.err }

// cannedBands answers the start and end queries of one metric with a series per band.
func cannedBands(f *fakeProm, metric string, labels []map[string]string, starts, ends []float64) {
	var s, e []any
	for i, l := range labels {
		s = append(s, sample(l, starts[i]))
		e = append(e, sample(l, ends[i]))
	}
	f.instant = append(f.instant,
		answer{match: "min_over_time(timestamp(" + metric, result: s},
		answer{match: "max_over_time(timestamp(" + metric, result: e})
}

func runLabels(id, kind, site string) map[string]string {
	return map[string]string{"run": id, "kind": kind, "site": site}
}

func fixture(t *testing.T) (*fakeProm, Querier) {
	f, q, _ := newFake(t)
	cannedBands(f, "concierge_run_active",
		[]map[string]string{runLabels("7", "errand", "shop.example"), runLabels("8", "check", "gone.example")},
		[]float64{evTo - 1800, evTo - 900}, []float64{evTo - 1560, evTo - 780})
	cannedBands(f, "concierge_call_active",
		[]map[string]string{{"call": "3", "direction": "in"}},
		[]float64{evTo - 600}, []float64{evTo - 540})
	return f, q
}

func goodRuns() fakeRunStore {
	return fakeRunStore{runs: map[int64]signins.Run{
		7: {ID: 7, Site: "shop.example", Kind: "errand", State: "ended", Reason: "booked"},
	}}
}

func goodCalls() fakeCallLister {
	return fakeCallLister{calls: []CallInfo{{ID: 3, Number: "+351900000000", Direction: "in", Outcome: "answered"}}}
}

func byLabelPrefix(evs []Event, prefix string) *Event {
	for i := range evs {
		if strings.HasPrefix(evs[i].Label, prefix) {
			return &evs[i]
		}
	}
	return nil
}

func TestEventsEnrichedAndPruned(t *testing.T) {
	_, q := fixture(t)
	evs, err := Events(context.Background(), q, goodRuns(), goodCalls(), "overview", evFrom, evEnd)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("events = %+v", evs)
	}
	e := byLabelPrefix(evs, "Errand:")
	if e == nil || e.Kind != "errand" || e.Label != "Errand: shop.example, 4 min, booked" || e.Link != "/errands/runs/7" {
		t.Errorf("errand = %+v", e)
	}
	if e.End == nil || !e.End.Equal(time.Unix(evTo-1560, 0)) || !e.Start.Equal(time.Unix(evTo-1800, 0)) {
		t.Errorf("errand times = %v – %v", e.Start, e.End)
	}
	s := byLabelPrefix(evs, "Sign-in")
	if s == nil || s.Kind != "signin" || s.Label != "Sign-in, 2 min" || s.Link != "" {
		t.Errorf("pruned = %+v", s)
	}
	c := byLabelPrefix(evs, "Call")
	if c == nil || c.Label != "Call in +351900000000, 1 min, answered" || c.Link != "/voice" {
		t.Errorf("call = %+v", c)
	}
}

func TestEventsQueryRange(t *testing.T) {
	f, q := fixture(t)
	if _, err := Events(context.Background(), q, nil, nil, "overview", evFrom, evEnd); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range f.asked() {
		if s == "min_over_time(timestamp(concierge_run_active)[3600s:10s])" {
			found = true
		}
	}
	if !found {
		t.Errorf("queries = %q", f.asked())
	}
}

func TestEventsOpenRun(t *testing.T) {
	f, q, _ := newFake(t)
	cannedBands(f, "concierge_run_active", []map[string]string{runLabels("7", "errand", "shop.example")},
		[]float64{evTo - 300}, []float64{evTo - 10})
	runs := fakeRunStore{runs: map[int64]signins.Run{7: {ID: 7, Site: "shop.example", Kind: "errand", State: "running"}}}
	evs, err := Events(context.Background(), q, runs, goodCalls(), "overview", evFrom, evEnd)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].End != nil || !strings.Contains(evs[0].Label, "running") {
		t.Fatalf("events = %+v", evs)
	}
}

func TestEventsReboots(t *testing.T) {
	f, q, _ := newFake(t)
	t1, t2 := float64(evTo-2000), float64(evTo-500)
	f.instant = []answer{{match: "node_boot_time_seconds", result: []any{
		sample(map[string]string{"instance": "a"}, t1),
		sample(map[string]string{"instance": "b"}, t1),
		sample(map[string]string{"instance": "c"}, t2),
		sample(map[string]string{"instance": "d"}, evTo-7200), // before from
	}}}
	evs, err := Events(context.Background(), q, nil, nil, "sms", evFrom, evEnd)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("events = %+v", evs)
	}
	for _, e := range evs {
		if e.Kind != "reboot" || e.End == nil || !e.End.Equal(e.Start) || e.Link != "" {
			t.Errorf("reboot = %+v", e)
		}
	}
	if !evs[1].Start.Equal(time.Unix(int64(t2), 0)) {
		t.Errorf("last reboot = %v", evs[1].Start)
	}
}

func TestEventsTabFilter(t *testing.T) {
	for tab, want := range map[string][]string{
		"calls":    {"call"},
		"sms":      {"reboot"},
		"browser":  {"errand", "signin"},
		"overview": {"call", "errand", "reboot", "signin"},
	} {
		f, q := fixture(t)
		f.instant = append(f.instant, answer{match: "node_boot_time_seconds", result: []any{
			sample(map[string]string{}, evTo-100)}})
		evs, err := Events(context.Background(), q, goodRuns(), goodCalls(), tab, evFrom, evEnd)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range evs {
			got = append(got, e.Kind)
		}
		if strings.Join(sorted(got), ",") != strings.Join(want, ",") {
			t.Errorf("%s: kinds = %v, want %v", tab, got, want)
		}
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestEventsEnrichmentFailure(t *testing.T) {
	_, q := fixture(t)
	evs, err := Events(context.Background(), q, fakeRunStore{err: errors.New("db")}, fakeCallLister{err: errors.New("down")},
		"overview", evFrom, evEnd)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("events = %+v", evs)
	}
	if c := byLabelPrefix(evs, "Call"); c == nil || c.Label != "Call, 1 min" || c.Link != "" {
		t.Errorf("call = %+v", c)
	}
	if e := byLabelPrefix(evs, "Errand"); e == nil || e.Label != "Errand, 4 min" {
		t.Errorf("errand = %+v", e)
	}
}

func TestEventsHandler(t *testing.T) {
	f, q := fixture(t)
	h := Handler(q, goodRuns(), goodCalls())
	var got struct {
		Events []struct {
			Kind  string `json:"kind"`
			Start int64  `json:"start"`
			End   *int64 `json:"end"`
			Label string `json:"label"`
			Link  string `json:"link"`
		} `json:"events"`
	}
	if code := getJSON(t, h, "/api/v1/health/events?tab=calls&from=1700096400&to=1700100000", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Events) != 1 || got.Events[0].Kind != "call" || got.Events[0].Start != evTo-600 ||
		got.Events[0].End == nil || *got.Events[0].End != evTo-540 {
		t.Errorf("events = %+v", got.Events)
	}
	for target, want := range map[string]int{
		"/api/v1/health/events?tab=nope&from=1700096400&to=1700100000": 404,
		"/api/v1/health/events?tab=calls&from=5&to=5":                  400,
		"/api/v1/health/events?tab=calls&from=0&to=" + "3000000":       400,
	} {
		if code, _ := getRaw(t, h, target); code != want {
			t.Errorf("%s: %d, want %d", target, code, want)
		}
	}
	f.status = 500
	if code, _ := getRaw(t, h, "/api/v1/health/events?tab=calls&from=1700096400&to=1700100000"); code != 503 {
		t.Errorf("prometheus down: %d", code)
	}
	var empty struct{ Events json.RawMessage }
	f.status = 0
	f.instant = nil
	getJSON(t, h, "/api/v1/health/events?tab=calls&from=1700096400&to=1700100000", &empty)
	if string(empty.Events) != "[]" {
		t.Errorf("no events = %s, want []", empty.Events)
	}
}

func TestCallListerOverSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "hl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var query string
	srv := &http.Server{Protocols: h2c.Protocols(), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Path + "?" + r.URL.RawQuery
		w.Write([]byte(`[{"id":3,"number":"+3519","direction":"out","started_at":"x","outcome":"missed","summary":"s"}]`))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	got, err := NewCallLister(sock).Calls(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (CallInfo{ID: 3, Number: "+3519", Direction: "out", Outcome: "missed"}) {
		t.Errorf("calls = %+v", got)
	}
	if query != "/calls?limit=5000" {
		t.Errorf("asked %s", query)
	}
	if _, err := NewCallLister(filepath.Join(dir, "none.sock")).Calls(context.Background()); err == nil {
		t.Error("a missing socket should error")
	}
}
