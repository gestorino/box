package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// maxRange is Prometheus's retention: older samples don't exist, so a longer range is refused.
const maxRange = 30 * 24 * time.Hour

// nowWindow is the rate window of the tiles: three 10 s scrapes.
const nowWindow = 30 * time.Second

type tileJSON struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Value *float64 `json:"value"`
	Unit  Unit     `json:"unit"`
	Total *float64 `json:"total"`
	Note  *string  `json:"note"`
}

type fanJSON struct {
	Output    int      `json:"output"`
	Automatic bool     `json:"automatic"`
	ManualPWM *float64 `json:"manual_pwm"`
}

type hardwareJSON struct {
	FanOutputs  []fanJSON `json:"fan_outputs"`
	PowerLimits struct {
		LongTerm  *float64 `json:"long_term"`
		ShortTerm *float64 `json:"short_term"`
	} `json:"power_limits"`
	CStateLimit  *int  `json:"cstate_limit"`
	CStateLocked *bool `json:"cstate_locked"`
}

type nowResponse struct {
	Running  *bool         `json:"running"`
	Tiles    []tileJSON    `json:"tiles"`
	Hardware *hardwareJSON `json:"hardware"`
}

type seriesJSON struct {
	Name   string     `json:"name"`
	Values []*float64 `json:"values"`
}

type lineJSON struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

type panelJSON struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Unit    Unit         `json:"unit"`
	Stacked bool         `json:"stacked"`
	Times   []int64      `json:"times"`
	Series  []seriesJSON `json:"series"`
	Lines   []lineJSON   `json:"lines"`
}

type panelsResponse struct {
	Step   int64       `json:"step"`
	Panels []panelJSON `json:"panels"`
}

type handler struct{ q Querier }

// Handler serves the Health page's named data, read from Prometheus: GET /api/v1/health/now
// (a tab's tiles) and GET /api/v1/health/panels (a tab's graphs over a range). The browser never
// sends a query.
func Handler(q Querier) http.Handler {
	h := handler{q}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health/now", h.now)
	mux.HandleFunc("GET /api/v1/health/panels", h.panels)
	return mux
}

func (h handler) now(w http.ResponseWriter, r *http.Request) {
	def, ok := catalog(r.URL.Query().Get("tab"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such tab"})
		return
	}
	ctx, at := r.Context(), time.Now()
	resp := nowResponse{Tiles: []tileJSON{}}
	running := true
	if def.procs != "" {
		procs, err := h.value(ctx, def.procs, at)
		if err != nil {
			unavailable(w, err)
			return
		}
		running = procs != nil && *procs > 0
		resp.Running = &running
	}
	for _, t := range def.tiles {
		tj := tileJSON{ID: t.id, Label: t.label, Unit: t.unit}
		if running {
			var err error
			if tj.Value, err = h.value(ctx, expand(t.value, nowWindow), at); err != nil {
				unavailable(w, err)
				return
			}
			if t.total != "" {
				if tj.Total, err = h.value(ctx, t.total, at); err != nil {
					unavailable(w, err)
					return
				}
			}
		}
		resp.Tiles = append(resp.Tiles, tj)
	}
	if r.URL.Query().Get("tab") == "hardware" {
		hw, err := h.hardware(ctx, at)
		if err != nil {
			unavailable(w, err)
			return
		}
		resp.Hardware = hw
	}
	writeJSON(w, http.StatusOK, resp)
}

// hardware is sensord's state: the fan outputs, the power limits and the package C-state limit.
func (h handler) hardware(ctx context.Context, at time.Time) (*hardwareJSON, error) {
	hw := &hardwareJSON{FanOutputs: []fanJSON{}}
	autos, err := h.q.Instant(ctx, fanAuto, at)
	if err != nil {
		return nil, err
	}
	pwms, err := h.q.Instant(ctx, fanPWM, at)
	if err != nil {
		return nil, err
	}
	for _, a := range autos {
		n, err := strconv.Atoi(a.Labels["output"])
		if err != nil {
			continue
		}
		fan := fanJSON{Output: n, Automatic: a.Value == 1}
		for _, p := range pwms {
			if p.Labels["output"] == a.Labels["output"] {
				fan.ManualPWM = finite(p.Value)
			}
		}
		hw.FanOutputs = append(hw.FanOutputs, fan)
	}
	slices.SortFunc(hw.FanOutputs, func(a, b fanJSON) int { return a.Output - b.Output })

	if hw.PowerLimits.LongTerm, err = h.value(ctx, limitLong, at); err != nil {
		return nil, err
	}
	if hw.PowerLimits.ShortTerm, err = h.value(ctx, limitShort, at); err != nil {
		return nil, err
	}
	limit, err := h.value(ctx, cstate, at)
	if err != nil {
		return nil, err
	}
	if limit != nil {
		n := int(*limit)
		hw.CStateLimit = &n
	}
	locked, err := h.value(ctx, cstateLock, at)
	if err != nil {
		return nil, err
	}
	if locked != nil {
		b := *locked == 1
		hw.CStateLocked = &b
	}
	return hw, nil
}

func (h handler) panels(w http.ResponseWriter, r *http.Request) {
	def, ok := catalog(r.URL.Query().Get("tab"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such tab"})
		return
	}
	from, to, ok := parseRange(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from and to must be unix seconds, from before to, at most 30 days apart"})
		return
	}
	step := Step(from, to)
	window := rateWindow(step)
	times := []int64{}
	for t := from; !t.After(to); t = t.Add(step) {
		times = append(times, t.Unix())
	}

	resp := panelsResponse{Step: int64(step / time.Second), Panels: []panelJSON{}}
	for _, p := range def.panels {
		pj := panelJSON{ID: p.id, Title: p.title, Unit: p.unit, Stacked: p.stacked, Times: times,
			Series: []seriesJSON{}, Lines: []lineJSON{}}
		for _, src := range p.series {
			got, err := h.q.Range(r.Context(), expand(src.expr, window), from, to, step)
			if err != nil {
				unavailable(w, err)
				return
			}
			if src.nameBy == nil {
				var points []Point
				if len(got) > 0 {
					points = got[0].Points
				}
				pj.Series = append(pj.Series, seriesJSON{Name: src.name, Values: align(points, from, step, len(times))})
				continue
			}
			named := make([]seriesJSON, 0, len(got))
			for _, s := range got {
				named = append(named, seriesJSON{Name: src.nameBy(s.Labels), Values: align(s.Points, from, step, len(times))})
			}
			slices.SortFunc(named, func(a, b seriesJSON) int { return seriesOrder(a.Name, b.Name) })
			pj.Series = append(pj.Series, named...)
		}
		for _, l := range p.lines {
			v, err := h.value(r.Context(), l.expr, to)
			if err != nil {
				unavailable(w, err)
				return
			}
			if v != nil {
				pj.Lines = append(pj.Lines, lineJSON{Name: l.name, Value: *v})
			}
		}
		resp.Panels = append(resp.Panels, pj)
	}
	writeJSON(w, http.StatusOK, resp)
}

// align puts points on the graph's times (from + k·step): a time with no point is null, so a
// stopped daemon or a missed scrape is a gap, never a zero.
func align(points []Point, from time.Time, step time.Duration, n int) []*float64 {
	out := make([]*float64, n)
	for _, p := range points {
		off := p.T.Sub(from)
		if off < 0 || off%step != 0 {
			continue
		}
		if i := int(off / step); i < n {
			out[i] = finite(p.V)
		}
	}
	return out
}

func parseRange(r *http.Request) (from, to time.Time, ok bool) {
	f, err1 := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	t, err2 := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if err1 != nil || err2 != nil || f >= t || time.Duration(t-f)*time.Second > maxRange {
		return time.Time{}, time.Time{}, false
	}
	return time.Unix(f, 0), time.Unix(t, 0), true
}

// value is a query's first sample at an instant; nil when it has none (a source not reporting).
func (h handler) value(ctx context.Context, expr string, at time.Time) (*float64, error) {
	got, err := h.q.Instant(ctx, expr, at)
	if err != nil || len(got) == 0 {
		return nil, err
	}
	return finite(got[0].Value), nil
}

// finite is v, or nil for NaN and ±Inf, which JSON can't carry and the page can't draw.
func finite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func unavailable(w http.ResponseWriter, err error) {
	slog.Warn("health: prometheus", "err", err)
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "health data unavailable"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
