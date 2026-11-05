// Package health serves what the Health page graphs: the engine's own activity for Prometheus, and
// the page's API.
package health

import (
	"context"
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"concierge/engine/internal/signins"
)

// RunLister is what the activity endpoint reads: the runs still running.
type RunLister interface {
	Running(context.Context) ([]signins.Run, error)
}

var runActive = prometheus.NewDesc("concierge_run_active",
	"1 for each run (errand, registration, check) that is running now.", []string{"run", "kind", "site"}, nil)

// runCollector asks the lister afresh on every scrape, so a run that ended has no series.
type runCollector struct{ runs RunLister }

func (runCollector) Describe(ch chan<- *prometheus.Desc) { ch <- runActive }

func (c runCollector) Collect(ch chan<- prometheus.Metric) {
	runs, err := c.runs.Running(context.Background())
	if err != nil {
		ch <- prometheus.NewInvalidMetric(runActive, err)
		return
	}
	for _, r := range runs {
		ch <- prometheus.MustNewConstMetric(runActive, prometheus.GaugeValue, 1,
			strconv.FormatInt(r.ID, 10), r.Kind, r.Site)
	}
}

// ActivityHandler serves the running runs as Prometheus exposition at /metrics; a lister error is a 500.
func ActivityHandler(runs RunLister) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(runCollector{runs})
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return mux
}
