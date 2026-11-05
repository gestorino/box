package health

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// Querier is what the Health handlers read Prometheus through.
type Querier interface {
	Instant(ctx context.Context, query string, at time.Time) ([]Sample, error)
	Range(ctx context.Context, query string, from, to time.Time, step time.Duration) ([]Series, error)
}

// Sample is one series' value at an instant.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Series is one series' points over a range.
type Series struct {
	Labels map[string]string
	Points []Point
}

// Point is one value of a Series.
type Point struct {
	T time.Time
	V float64
}

type promQuerier struct{ api v1.API }

// NewQuerier reads the Prometheus at baseURL (http://127.0.0.1:9090) over its HTTP API.
func NewQuerier(baseURL string) (Querier, error) {
	c, err := api.NewClient(api.Config{Address: baseURL})
	if err != nil {
		return nil, err
	}
	return promQuerier{v1.NewAPI(c)}, nil
}

func (p promQuerier) Instant(ctx context.Context, query string, at time.Time) ([]Sample, error) {
	v, _, _, err := p.api.Query(ctx, query, at)
	if err != nil {
		return nil, err
	}
	vec, ok := v.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("query %q: got %s, want a vector", query, v.Type())
	}
	out := make([]Sample, 0, len(vec))
	for _, s := range vec {
		out = append(out, Sample{Labels: labels(s.Metric), Value: float64(s.Value)})
	}
	return out, nil
}

func (p promQuerier) Range(ctx context.Context, query string, from, to time.Time, step time.Duration) ([]Series, error) {
	v, _, _, err := p.api.QueryRange(ctx, query, v1.Range{Start: from, End: to, Step: step})
	if err != nil {
		return nil, err
	}
	mat, ok := v.(model.Matrix)
	if !ok {
		return nil, fmt.Errorf("query %q: got %s, want a matrix", query, v.Type())
	}
	out := make([]Series, 0, len(mat))
	for _, s := range mat {
		points := make([]Point, 0, len(s.Values))
		for _, p := range s.Values {
			points = append(points, Point{T: p.Timestamp.Time(), V: float64(p.Value)})
		}
		out = append(out, Series{Labels: labels(s.Metric), Points: points})
	}
	return out, nil
}

func labels(m model.Metric) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[string(k)] = string(v)
	}
	return out
}
