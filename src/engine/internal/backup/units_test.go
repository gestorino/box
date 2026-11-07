package backup

import (
	"slices"
	"testing"
)

func TestUnitsStopOrder(t *testing.T) {
	n := len(Units)
	if Units[0] != "concierge-engine.service" {
		t.Errorf("first unit = %s; the engine stops first", Units[0])
	}
	if Units[n-2] != "concierge-secrets.service" || Units[n-1] != "concierge-secrets.socket" {
		t.Errorf("last units = %s, %s; secretd stops last", Units[n-2], Units[n-1])
	}
	seen := map[string]bool{}
	for _, u := range Units {
		if seen[u] {
			t.Errorf("%s listed twice", u)
		}
		seen[u] = true
	}
	if n != 17 {
		t.Errorf("%d units; want 17", n)
	}
}

// The core starts in this order; hostd then starts the added connectors itself, as at boot.
func TestCoreUnitsStartOrder(t *testing.T) {
	want := []string{
		"concierge-secrets.socket",
		"concierge-secrets.service",
		"concierge-hostd.socket",
		"concierge-hostd-settings.socket",
		"concierge-hostd.service",
		"concierge-worker.service",
		"concierge-engine.service",
	}
	if !slices.Equal(CoreUnits, want) {
		t.Fatalf("CoreUnits = %q, want %q", CoreUnits, want)
	}
	for _, u := range CoreUnits {
		if !slices.Contains(Units, u) {
			t.Errorf("%s is not one of Units: a backup would not stop it", u)
		}
	}
}
