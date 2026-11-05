package calls

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	metricsEvery    = 10 * time.Second // the scrape interval
	qtempEvery      = time.Minute      // the modem's temperature moves slowly; the port is shared
	metricsTextfile = "concierge-modem.prom"
)

// MetricsWriter writes calld's metrics for node_exporter's textfile collector: the calls in
// progress, and the modem's temperatures. The AT port is never queried while a call is live
// (infra/bkhd-1264/README.md, journals 12 and 15).
type MetricsWriter struct {
	Dir   string
	Live  func(context.Context) ([]LiveCall, error)
	QTemp func(context.Context) (string, error)
	Now   func() time.Time

	temps     map[string]float64 // the last good reading, by sensor
	lastQuery time.Time          // the last attempt, good or not
}

// Run writes the file every 10 s until ctx ends.
func (w *MetricsWriter) Run(ctx context.Context) {
	ticker := time.NewTicker(metricsEvery)
	defer ticker.Stop()
	for {
		w.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *MetricsWriter) tick(ctx context.Context) {
	live, err := w.Live(ctx)
	if err != nil {
		slog.Warn("calld: live calls for metrics", "err", err)
	}
	if err == nil && len(live) == 0 && (w.lastQuery.IsZero() || w.Now().Sub(w.lastQuery) >= qtempEvery) {
		w.lastQuery = w.Now()
		reply, err := w.QTemp(ctx)
		if err == nil {
			var pmic, xo, pa float64
			if pmic, xo, pa, err = parseQTEMP(reply); err == nil {
				w.temps = map[string]float64{"pmic": pmic, "xo": xo, "pa": pa}
			}
		}
		if err != nil {
			slog.Warn("calld: modem temperature", "err", err)
		}
	}

	reg := prometheus.NewRegistry()
	active := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "concierge_call_active", Help: "A call in progress."}, []string{"call", "direction"})
	temp := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "concierge_modem_temp_celsius", Help: "The modem's temperature, by sensor."}, []string{"sensor"})
	reg.MustRegister(active, temp)
	for _, c := range live {
		dir := "out"
		if c.Direction == "incoming" {
			dir = "in"
		}
		active.WithLabelValues(strconv.FormatInt(c.ID, 10), dir).Set(1)
	}
	for sensor, v := range w.temps {
		temp.WithLabelValues(sensor).Set(v)
	}
	if err := prometheus.WriteToTextfile(filepath.Join(w.Dir, metricsTextfile), reg); err != nil {
		slog.Warn("calld: write metrics", "err", err)
	}
}
