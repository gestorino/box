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
