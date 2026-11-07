package backup

import "testing"

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
