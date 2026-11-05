package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"concierge/engine/internal/h2c"
	"concierge/engine/internal/signins"
)

// RunStore is where a band's run is looked up: its site, state and reason.
type RunStore interface {
	RunsByID(context.Context, []int64) (map[int64]signins.Run, error)
}

// CallLister is where a band's call is looked up: its number, direction and outcome.
type CallLister interface {
	Calls(ctx context.Context) ([]CallInfo, error)
}

// CallInfo is what calld knows of a call that the bands show.
type CallInfo struct {
	ID                         int64
	Number, Direction, Outcome string // Direction is calld's: incoming | outgoing
}

type callClient struct{ c *http.Client }

// NewCallLister asks calld for its calls, over its Unix socket.
func NewCallLister(socket string) CallLister {
	return callClient{&http.Client{Transport: h2c.Transport(socket), Timeout: 10 * time.Second}}
}

func (l callClient) Calls(ctx context.Context) ([]CallInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://calld/calls?limit=5000", nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("calld: status %d", resp.StatusCode)
	}
	var rows []struct {
		ID        int64  `json:"id"`
		Number    string `json:"number"`
		Direction string `json:"direction"`
		Outcome   string `json:"outcome"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	out := make([]CallInfo, len(rows))
	for i, r := range rows {
		out[i] = CallInfo{r.ID, r.Number, r.Direction, r.Outcome}
	}
	return out, nil
}

// Event is a band on the graphs: an errand, a sign-in run, a call, or a reboot (Start == End).
// End is nil while it is still going.
type Event struct {
	Kind  string // errand | signin | call | reboot
	Start time.Time
	End   *time.Time
	Label string
	Link  string // empty when the record is gone, and for a reboot
}

type eventJSON struct {
	Kind  string `json:"kind"`
	Start int64  `json:"start"`
	End   *int64 `json:"end"`
	Label string `json:"label"`
	Link  string `json:"link"`
}

// openSlack is how close to the range's end a band's last sample must be to count as still going.
const openSlack = 20 * time.Second

type band struct {
	labels     map[string]string
	start, end time.Time
}

// Events are the bands of a tab over [from, to]. A band's times are exact sample times from
// Prometheus, so short activity survives a long range. Prometheus errors fail it; a RunStore or
// CallLister error only leaves the labels bare.
func Events(ctx context.Context, q Querier, runs RunStore, calls CallLister, tab string, from, to time.Time) ([]Event, error) {
	kinds := eventKinds(tab)
	var out []Event

	if kinds["errand"] || kinds["signin"] {
		bs, err := bands(ctx, q, "concierge_run_active", from, to)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, 0, len(bs))
		for _, b := range bs {
			if id, err := strconv.ParseInt(b.labels["run"], 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		var known map[int64]signins.Run
		if runs != nil && len(ids) > 0 {
			var err error
			if known, err = runs.RunsByID(ctx, ids); err != nil {
				slog.Warn("health: runs", "err", err)
			}
		}
		for _, b := range bs {
			kind := "signin"
			word := "Sign-in"
			if b.labels["kind"] == "errand" {
				kind, word = "errand", "Errand"
			}
			if !kinds[kind] {
				continue
			}
			id, _ := strconv.ParseInt(b.labels["run"], 10, 64)
			r, found := known[id]
			ev := Event{Kind: kind, Start: b.start, End: &b.end}
			if !found {
				ev.Label = fmt.Sprintf("%s, %s", word, duration(b.start, b.end, false))
			} else {
				open := r.State == "running" && to.Sub(b.end) <= openSlack
				why := r.Reason
				if why == "" {
					why = r.State
				}
				ev.Label = fmt.Sprintf("%s: %s, %s, %s", word, r.Site, duration(b.start, b.end, open), why)
				if kind == "errand" {
					ev.Link = fmt.Sprintf("/errands/runs/%d", id)
				} else {
					ev.Link = fmt.Sprintf("/accounts/%s/runs/%d", r.Site, id)
				}
				if open {
					ev.End = nil
				}
			}
			out = append(out, ev)
		}
	}

	if kinds["call"] {
		bs, err := bands(ctx, q, "concierge_call_active", from, to)
		if err != nil {
			return nil, err
		}
		known := map[int64]CallInfo{}
		if calls != nil && len(bs) > 0 {
			list, err := calls.Calls(ctx)
			if err != nil {
				slog.Warn("health: calls", "err", err)
			}
			for _, c := range list {
				known[c.ID] = c
			}
		}
		for _, b := range bs {
			id, _ := strconv.ParseInt(b.labels["call"], 10, 64)
			c, found := known[id]
			ev := Event{Kind: "call", Start: b.start, End: &b.end}
			if !found {
				ev.Label = "Call, " + duration(b.start, b.end, false)
			} else {
				open := (c.Outcome == "ringing" || c.Outcome == "in-progress") && to.Sub(b.end) <= openSlack
				ev.Label = fmt.Sprintf("Call %s %s, %s, %s", b.labels["direction"], c.Number, duration(b.start, b.end, open), c.Outcome)
				ev.Link = "/voice"
				if open {
					ev.End = nil
				}
			}
			out = append(out, ev)
		}
	}

	if kinds["reboot"] {
		// node_exporter has one boot-time series whose value changes in place at a reboot, so read
		// the range, not an instant: every distinct value inside it is a reboot.
		step := Step(from, to)
		w := int64(gaugeWindow(step) / time.Second)
		seen := map[int64]bool{}
		for _, fn := range []string{"min_over_time", "max_over_time"} {
			got, err := q.Range(ctx, fmt.Sprintf("%s(node_boot_time_seconds[%ds])", fn, w), from, to, step)
			if err != nil {
				return nil, err
			}
			for _, s := range got {
				for _, p := range s.Points {
					t := int64(p.V)
					if seen[t] || t < from.Unix() || t > to.Unix() {
						continue
					}
					seen[t] = true
					at := time.Unix(t, 0)
					out = append(out, Event{Kind: "reboot", Start: at, End: &at, Label: "Reboot"})
				}
			}
		}
	}

	slices.SortStableFunc(out, func(a, b Event) int { return a.Start.Compare(b.Start) })
	return out, nil
}

// bands is one band per series of a 0/1 activity metric whose labels name its run or call: the
// first and last sample times over the range, evaluated at its end.
func bands(ctx context.Context, q Querier, metric string, from, to time.Time) ([]band, error) {
	span := int64((to.Sub(from) + time.Second - 1) / time.Second)
	query := func(fn string) ([]Sample, error) {
		return q.Instant(ctx, fmt.Sprintf("%s(timestamp(%s)[%ds:10s])", fn, metric, span), to)
	}
	starts, err := query("min_over_time")
	if err != nil {
		return nil, err
	}
	ends, err := query("max_over_time")
	if err != nil {
		return nil, err
	}
	endOf := map[string]float64{}
	for _, s := range ends {
		endOf[labelKey(s.Labels)] = s.Value
	}
	var out []band
	for _, s := range starts {
		e, ok := endOf[labelKey(s.Labels)]
		if !ok {
			continue
		}
		out = append(out, band{s.Labels, time.Unix(int64(s.Value), 0), time.Unix(int64(e), 0)})
	}
	return out, nil
}

func labelKey(l map[string]string) string {
	b, _ := json.Marshal(l) // map keys are sorted
	return string(b)
}

// eventKinds are the kinds of band a tab shows.
func eventKinds(tab string) map[string]bool {
	switch tab {
	case "overview", "hardware":
		return map[string]bool{"errand": true, "signin": true, "call": true, "reboot": true}
	case "browser":
		return map[string]bool{"errand": true, "signin": true}
	case "calls":
		return map[string]bool{"call": true}
	}
	return map[string]bool{"reboot": true}
}

// duration is how long a band lasted, in words: "running" while it still goes.
func duration(start, end time.Time, open bool) string {
	d := end.Sub(start)
	switch {
	case open:
		return "running"
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d/time.Minute))
	case d%time.Hour < time.Minute:
		return fmt.Sprintf("%d h", int(d/time.Hour))
	}
	return fmt.Sprintf("%d h %d min", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

func (h handler) events(w http.ResponseWriter, r *http.Request) {
	tab := r.URL.Query().Get("tab")
	if _, ok := catalog(tab); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such tab"})
		return
	}
	from, to, ok := parseRange(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from and to must be unix seconds, from before to, at most 30 days apart"})
		return
	}
	evs, err := Events(r.Context(), h.q, h.runs, h.calls, tab, from, to)
	if err != nil {
		unavailable(w, err)
		return
	}
	resp := struct {
		Events []eventJSON `json:"events"`
	}{[]eventJSON{}}
	for _, e := range evs {
		ej := eventJSON{Kind: e.Kind, Start: e.Start.Unix(), Label: e.Label, Link: e.Link}
		if e.End != nil {
			end := e.End.Unix()
			ej.End = &end
		}
		resp.Events = append(resp.Events, ej)
	}
	writeJSON(w, http.StatusOK, resp)
}
