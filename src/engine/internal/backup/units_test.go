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
	if n != 24 {
		t.Errorf("%d units; want 24", n)
	}
	// sensord reads the state directories (CAP_DAC_READ_SEARCH) through mounts on the old ones: it
	// must not outlive a swap, and stops before secretd as everything else does.
	if i := slices.Index(Units, "concierge-sensors.service"); i < 0 || i >= n-2 {
		t.Errorf("concierge-sensors.service at %d; want it stopped before secretd", i)
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
		"concierge-sensors.service",
		"concierge-worker.service",
		"concierge-relay.socket",
		"concierge-relay.service",
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

// relayd reads secretd and always runs (hostd does not start it): it stops before secretd, and
// the backup starts it again with the core.
func TestUnitsStopRelayBeforeSecretd(t *testing.T) {
	svc, sock := slices.Index(Units, "concierge-relay.service"), slices.Index(Units, "concierge-relay.socket")
	secrets := slices.Index(Units, "concierge-secrets.service")
	if svc < 0 || sock != svc+1 || sock >= secrets {
		t.Fatalf("Units %v: want relay's service, then its socket, before secretd", Units)
	}
}
