package health

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// answer is a canned result for every query that contains match.
type answer struct {
	match  string
	result []any // vector or matrix entries, as Prometheus serialises them
}

// fakeProm answers /api/v1/query and /api/v1/query_range from canned answers, matched by substring
// in order; a query nothing matches gets an empty result. It records every query it was asked.
type fakeProm struct {
	mu      sync.Mutex
	instant []answer
	ranges  []answer
	queries []string
	status  int // when set, every request gets this status and no data
}

func (f *fakeProm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := r.Form.Get("query")
	f.mu.Lock()
	f.queries = append(f.queries, q)
	status := f.status
	answers, kind := f.instant, "vector"
	if r.URL.Path == "/api/v1/query_range" {
		answers, kind = f.ranges, "matrix"
	}
	result := []any{}
	for _, a := range answers {
		if strings.Contains(q, a.match) {
			result = a.result
			break
		}
	}
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "success",
		"data":   map[string]any{"resultType": kind, "result": result},
	})
}

func (f *fakeProm) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

func (f *fakeProm) forget() {
	f.mu.Lock()
	f.queries = nil
	f.mu.Unlock()
}

// sample is one vector entry.
func sample(labels map[string]string, v float64) any {
	return map[string]any{"metric": labels, "value": []any{1700000000.0, num(v)}}
}

// series is one matrix entry with a point at each time.
func series(labels map[string]string, times []int64, v func(int64) float64) any {
	values := make([]any, 0, len(times))
	for _, t := range times {
		values = append(values, []any{float64(t), num(v(t))})
	}
	return map[string]any{"metric": labels, "values": values}
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func newFake(t *testing.T) (*fakeProm, Querier, *httptest.Server) {
	t.Helper()
	f := &fakeProm{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	q, err := NewQuerier(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return f, q, srv
}

func TestQuerierReadsVectorsAndMatrices(t *testing.T) {
	f, q, _ := newFake(t)
	f.instant = []answer{{match: "up", result: []any{sample(map[string]string{"job": "node"}, 1)}}}
	f.ranges = []answer{{match: "up", result: []any{
		series(map[string]string{"job": "node"}, []int64{100, 110}, func(t int64) float64 { return float64(t) / 10 }),
	}}}

	got, err := q.Instant(context.Background(), "up", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Labels["job"] != "node" || got[0].Value != 1 {
		t.Fatalf("instant = %+v", got)
	}

	rs, err := q.Range(context.Background(), "up", time.Unix(100, 0), time.Unix(110, 0), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Labels["job"] != "node" || len(rs[0].Points) != 2 ||
		!rs[0].Points[1].T.Equal(time.Unix(110, 0)) || rs[0].Points[1].V != 11 {
		t.Fatalf("range = %+v", rs)
	}
}

func TestQuerierPassesNaN(t *testing.T) {
	f, q, _ := newFake(t)
	f.instant = []answer{{match: "x", result: []any{
		map[string]any{"metric": map[string]string{}, "value": []any{1700000000.0, "NaN"}},
	}}}
	got, err := q.Instant(context.Background(), "x", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !math.IsNaN(got[0].Value) {
		t.Fatalf("instant = %+v", got)
	}
}
