package health

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStep(t *testing.T) {
	from := time.Unix(1700000000, 0)
	for _, c := range []struct {
		span time.Duration
		want time.Duration
	}{
		{time.Hour, 10 * time.Second},
		{6 * time.Hour, 50 * time.Second},
		{30 * 24 * time.Hour, 5190 * time.Second},
	} {
		if got := Step(from, from.Add(c.span)); got != c.want {
			t.Errorf("Step(%v) = %v, want %v", c.span, got, c.want)
		}
	}
}

func TestPanelsCatalog(t *testing.T) {
	f, q, _ := newFake(t)
	h := Handler(q)
	service := []string{"cpu", "memory", "disk_io", "storage"}
	want := map[string][]string{
		"overview": {"cpu_by_service", "memory_by_service", "temperatures", "power", "clock", "storage_by_service", "disk_io"},
		"hardware": {"power", "clock", "temp_cpu", "temp_board", "temp_disk", "temp_wifi", "temp_modem", "load"},
	}
	for tab := range ServiceUser {
		want[tab] = service
	}
	if ServiceUser["whatsapp"] != "concierge-wa" {
		t.Fatalf("whatsapp runs as %q", ServiceUser["whatsapp"])
	}
	if len(want) != len(Tabs) {
		t.Fatalf("catalogue covers %d tabs, Tabs has %d", len(want), len(Tabs))
	}

	for _, tab := range Tabs {
		f.forget()
		var got panelsResponse
		if code := getJSON(t, h, "/api/v1/health/panels?tab="+tab+"&from=1700000000&to=1700003600", &got); code != 200 {
			t.Fatalf("%s: status %d", tab, code)
		}
		ids := []string{}
		for _, p := range got.Panels {
			ids = append(ids, p.ID)
		}
		if !slices.Equal(ids, want[tab]) {
			t.Errorf("%s panels = %v, want %v", tab, ids, want[tab])
		}

		user, ok := ServiceUser[tab]
		if !ok {
			continue
		}
		mentioned := false
		for _, query := range f.asked() {
			if strings.Contains(query, "namedprocess") {
				if !strings.Contains(query, `groupname="`+user+`"`) {
					t.Errorf("%s: query not limited to %s: %s", tab, user, query)
				}
				mentioned = true
			}
		}
		if !mentioned {
			t.Errorf("%s: no query mentions its user %s", tab, user)
		}
	}
}

// TestGaugesAggregateOverTheStep pins that a graph's gauge points summarise their whole step
// (max for heat and load, average for memory and clock), with the step itself as the window,
// while a tile reads the gauge's current value.
func TestGaugesAggregateOverTheStep(t *testing.T) {
	f, q, _ := newFake(t)
	h := Handler(q)
	gauges := []string{
		"node_hwmon_temp_celsius", "concierge_board_temp_celsius", "concierge_modem_temp_celsius",
		"namedprocess_namegroup_memory_bytes", "node_cpu_scaling_frequency_hertz", "node_load",
	}
	isGauge := func(query string) bool {
		return slices.ContainsFunc(gauges, func(g string) bool { return strings.Contains(query, g) })
	}
	want := map[string]string{ // the aggregation each gauge gets on a graph
		"node_hwmon_temp_celsius":             "max_over_time(node_hwmon_temp_celsius",
		"concierge_board_temp_celsius":        "max_over_time(concierge_board_temp_celsius",
		"concierge_modem_temp_celsius":        "max_over_time(concierge_modem_temp_celsius",
		"namedprocess_namegroup_memory_bytes": "avg_over_time(namedprocess_namegroup_memory_bytes",
		"node_load":                           "max_over_time(node_load",
	}
	for _, tab := range []string{"overview", "hardware", "whatsapp"} {
		// 1 h: the step is 10 s, below the 30 s rate window, so only the step can give [10s].
		f.forget()
		getRaw(t, h, "/api/v1/health/panels?tab="+tab+"&from=1700000000&to=1700003600")
		seen := 0
		for _, query := range f.asked() {
			if !isGauge(query) {
				continue
			}
			seen++
			if !strings.Contains(query, "[10s]") {
				t.Errorf("%s graph: window is not the step: %s", tab, query)
			}
			for g, agg := range want {
				if strings.Contains(query, g) && !strings.Contains(query, agg) {
					t.Errorf("%s graph: %s not under %s: %s", tab, g, agg, query)
				}
			}
			if strings.Contains(query, "node_cpu_scaling_frequency_hertz") &&
				!strings.Contains(query, "avg(avg_over_time(node_cpu_scaling_frequency_hertz") &&
				!strings.Contains(query, "max(max_over_time(node_cpu_scaling_frequency_hertz") {
				t.Errorf("%s graph: clock not aggregated over the step: %s", tab, query)
			}
		}
		if seen == 0 {
			t.Errorf("%s: no gauge graph queried", tab)
		}

		f.forget()
		f.instant = []answer{{match: "num_procs", result: []any{sample(nil, 1)}}}
		getRaw(t, h, "/api/v1/health/now?tab="+tab)
		f.instant = nil
		tiles := 0
		for _, query := range f.asked() {
			if isGauge(query) {
				tiles++
			}
			if strings.Contains(query, "_over_time") {
				t.Errorf("%s tile is not a current value: %s", tab, query)
			}
		}
		if tiles == 0 {
			t.Errorf("%s: no gauge tile queried", tab)
		}
	}
}
