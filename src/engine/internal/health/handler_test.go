package health

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func getRaw(t *testing.T, h http.Handler, target string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code, rec.Body.String()
}

func getJSON(t *testing.T, h http.Handler, target string, v any) int {
	t.Helper()
	code, body := getRaw(t, h, target)
	if code == 200 {
		if err := json.Unmarshal([]byte(body), v); err != nil {
			t.Fatalf("%s: %v in %s", target, err, body)
		}
	}
	return code
}

func TestPanelsAlignsAndNulls(t *testing.T) {
	f, q, _ := newFake(t)
	const from, to = 1700000000, 1700003600 // 1 h: step 10 s, 361 times
	var times []int64
	for t := int64(from); t <= to; t += 10 {
		if t != from+50 && t != from+1000 { // two missing samples
			times = append(times, t)
		}
	}
	f.ranges = []answer{{match: "concierge_package_energy_joules_total", result: []any{
		series(map[string]string{}, times, func(int64) float64 { return 5.5 }),
	}}}

	var got panelsResponse
	if code := getJSON(t, Handler(q), "/api/v1/health/panels?tab=overview&from=1700000000&to=1700003600", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.Step != 10 {
		t.Fatalf("step = %d", got.Step)
	}
	var power *panelJSON
	for i, p := range got.Panels {
		if len(p.Times) != 361 || p.Times[0] != from || p.Times[360] != to {
			t.Errorf("%s: %d times from %v", p.ID, len(p.Times), p.Times[:1])
		}
		for _, s := range p.Series {
			if len(s.Values) != len(p.Times) {
				t.Errorf("%s/%s: %d values for %d times", p.ID, s.Name, len(s.Values), len(p.Times))
			}
		}
		if p.ID == "power" {
			power = &got.Panels[i]
		}
	}
	if power == nil || len(power.Series) != 1 {
		t.Fatalf("power panel = %+v", power)
	}
	for i, v := range power.Series[0].Values {
		missing := power.Times[i] == from+50 || power.Times[i] == from+1000
		switch {
		case missing && v != nil:
			t.Errorf("t=%d: %v, want null", power.Times[i], *v)
		case !missing && (v == nil || *v != 5.5):
			t.Errorf("t=%d: %v, want 5.5", power.Times[i], v)
		}
	}
}

func TestNowServiceNotRunning(t *testing.T) {
	for name, procs := range map[string][]any{
		"zero processes": {sample(map[string]string{"groupname": "concierge-wa"}, 0)},
		"absent":         {},
	} {
		t.Run(name, func(t *testing.T) {
			f, q, _ := newFake(t)
			f.instant = []answer{
				{match: "namedprocess_namegroup_num_procs", result: procs},
				{match: "namedprocess_namegroup_memory_bytes", result: []any{sample(nil, 1<<20)}},
			}
			code, body := getRaw(t, Handler(q), "/api/v1/health/now?tab=whatsapp")
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			var got nowResponse
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatal(err)
			}
			if got.Running == nil || *got.Running {
				t.Fatalf("running = %v in %s", got.Running, body)
			}
			if len(got.Tiles) != 3 {
				t.Fatalf("tiles = %+v", got.Tiles)
			}
			for _, tile := range got.Tiles {
				if tile.Value != nil {
					t.Errorf("tile %s = %v, want null", tile.ID, *tile.Value)
				}
			}
			if !strings.Contains(body, `"hardware":null`) {
				t.Errorf("hardware block on a service tab: %s", body)
			}
		})
	}

	t.Run("running", func(t *testing.T) {
		f, q, _ := newFake(t)
		f.instant = []answer{
			{match: "namedprocess_namegroup_num_procs", result: []any{sample(nil, 2)}},
			{match: "namedprocess_namegroup_memory_bytes", result: []any{sample(nil, 1<<20)}},
		}
		var got nowResponse
		if code := getJSON(t, Handler(q), "/api/v1/health/now?tab=whatsapp", &got); code != 200 {
			t.Fatalf("status %d", code)
		}
		if got.Running == nil || !*got.Running {
			t.Fatalf("running = %v", got.Running)
		}
		ids := []string{}
		for _, tile := range got.Tiles {
			ids = append(ids, tile.ID)
			if tile.ID == "memory" && (tile.Value == nil || *tile.Value != 1<<20 || tile.Unit != "bytes") {
				t.Errorf("memory tile = %+v", tile)
			}
		}
		if strings.Join(ids, ",") != "cpu,memory,storage" {
			t.Errorf("tiles = %v", ids)
		}
	})
}

func TestNowHardwareBlock(t *testing.T) {
	f, q, _ := newFake(t)
	out := func(n string) map[string]string { return map[string]string{"output": n} }
	f.instant = []answer{
		{match: "concierge_fan_output_automatic", result: []any{
			sample(out("2"), 1), sample(out("1"), 0), sample(out("3"), 1),
		}},
		{match: "concierge_fan_output_manual_pwm_ratio", result: []any{
			sample(out("1"), 0), sample(out("2"), 0.47), sample(out("3"), 0.47),
		}},
		{match: `concierge_power_limit_watts{limit="long_term"}`, result: []any{sample(nil, 15)}},
		{match: `concierge_power_limit_watts{limit="short_term"}`, result: []any{sample(nil, 20)}},
		{match: "concierge_package_cstate_limit", result: []any{sample(nil, 0)}},
		{match: "concierge_package_cstate_locked", result: []any{sample(nil, 1)}},
	}
	code, body := getRaw(t, Handler(q), "/api/v1/health/now?tab=hardware")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, `"running":null`) {
		t.Errorf("running on the hardware tab: %s", body)
	}
	var got struct {
		Tiles    []tileJSON     `json:"tiles"`
		Hardware map[string]any `json:"hardware"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"fan_outputs": []any{
			map[string]any{"output": 1.0, "automatic": false, "manual_pwm": 0.0},
			map[string]any{"output": 2.0, "automatic": true, "manual_pwm": 0.47},
			map[string]any{"output": 3.0, "automatic": true, "manual_pwm": 0.47},
		},
		"power_limits":  map[string]any{"long_term": 15.0, "short_term": 20.0},
		"cstate_limit":  0.0,
		"cstate_locked": true,
	}
	if !reflect.DeepEqual(got.Hardware, want) {
		t.Errorf("hardware = %#v\nwant       %#v", got.Hardware, want)
	}
	ids := []string{}
	for _, tile := range got.Tiles {
		ids = append(ids, tile.ID)
	}
	if strings.Join(ids, ",") != "cpu_temp,power,clock,board_temp_max" {
		t.Errorf("tiles = %v", ids)
	}

	t.Run("no sensord", func(t *testing.T) {
		_, q, _ := newFake(t)
		_, body := getRaw(t, Handler(q), "/api/v1/health/now?tab=hardware")
		for _, want := range []string{
			`"fan_outputs":[]`,
			`"power_limits":{"long_term":null,"short_term":null}`,
			`"cstate_limit":null`,
			`"cstate_locked":null`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %s in %s", want, body)
			}
		}
	})
}

func TestNowOverview(t *testing.T) {
	f, q, _ := newFake(t)
	f.instant = []answer{
		{match: "node_memory_MemAvailable_bytes", result: []any{sample(nil, 3e9)}},
		{match: "node_memory_MemTotal_bytes", result: []any{sample(nil, 8e9)}},
	}
	code, body := getRaw(t, Handler(q), "/api/v1/health/now?tab=overview")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var got nowResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Running != nil || got.Hardware != nil {
		t.Errorf("running/hardware on overview: %s", body)
	}
	ids := []string{}
	for _, tile := range got.Tiles {
		ids = append(ids, tile.ID)
		if tile.ID == "memory" && (tile.Value == nil || *tile.Value != 3e9 || tile.Total == nil || *tile.Total != 8e9) {
			t.Errorf("memory tile = %s", body)
		}
		if tile.ID == "cpu" && tile.Value != nil {
			t.Errorf("cpu with no data = %v, want null", *tile.Value)
		}
	}
	if strings.Join(ids, ",") != "cpu,memory,disk,cpu_temp,power" {
		t.Errorf("tiles = %v", ids)
	}
}

func TestBadRequests(t *testing.T) {
	_, q, _ := newFake(t)
	h := Handler(q)
	for target, want := range map[string]int{
		"/api/v1/health/now?tab=nope": 404,
		"/api/v1/health/now":          404,
		"/api/v1/health/panels?tab=nope&from=1700000000&to=1700003600":     404,
		"/api/v1/health/panels?tab=overview&from=1700000000&to=1700000000": 400,
		"/api/v1/health/panels?tab=overview&from=1700003600&to=1700000000": 400,
		"/api/v1/health/panels?tab=overview&from=1700000000&to=1702678400": 400, // 31 d
		"/api/v1/health/panels?tab=overview&from=abc&to=1700003600":        400,
		"/api/v1/health/panels?tab=overview&from=1700000000":               400,
		"/api/v1/health/panels?tab=overview&from=1700000000&to=1702592000": 200, // exactly 30 d
	} {
		if code, body := getRaw(t, h, target); code != want {
			t.Errorf("%s: %d, want %d (%s)", target, code, want, body)
		}
	}
}

func TestPrometheusDown(t *testing.T) {
	const want = `{"error":"health data unavailable"}`
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	check := func(t *testing.T, h http.Handler) {
		t.Helper()
		logs.Reset()
		for _, target := range []string{
			"/api/v1/health/now?tab=overview",
			"/api/v1/health/now?tab=hardware",
			"/api/v1/health/now?tab=sms",
			"/api/v1/health/panels?tab=overview&from=1700000000&to=1700003600",
		} {
			code, body := getRaw(t, h, target)
			if code != 503 || strings.TrimSpace(body) != want {
				t.Errorf("%s: %d %s, want 503 %s", target, code, body, want)
			}
		}
		if !strings.Contains(logs.String(), "health: prometheus") {
			t.Errorf("the failure is not logged: %q", logs.String())
		}
	}
	t.Run("answers 500", func(t *testing.T) {
		f, q, _ := newFake(t)
		f.status = 500
		check(t, Handler(q))
	})
	t.Run("closed", func(t *testing.T) {
		_, q, srv := newFake(t)
		srv.Close()
		check(t, Handler(q))
	})
}
