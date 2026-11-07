package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const callPrefix = "/org/freedesktop/ModemManager1/Call/"

// Busy says what keeps a backup or restore waiting: "" when the box is idle,
// else the reason. A run holds a screen under root/run/concierge-worker/screens,
// named by its site; a call is one mmcli lists.
func Busy(sys System, root string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "run/concierge-worker/screens"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var reasons []string
	for _, e := range entries {
		reasons = append(reasons, "a run is in progress at "+e.Name())
	}
	reason := strings.Join(reasons, "; ")
	if len(entries) > 0 {
		up, err := sys.Active("concierge-worker.service")
		if err != nil {
			return "", err
		}
		if !up {
			reason += "; restart the worker to clear it"
		}
	}

	out, err := sys.Calls()
	if err != nil {
		return "", fmt.Errorf("listing calls: %w", err)
	}
	if len(parseCalls(out)) > 0 {
		if reason != "" {
			reason += "; "
		}
		reason += "a call is in progress"
	}
	return reason, nil
}

// parseCalls returns the call paths in mmcli's --voice-list-calls output.
// "No calls were found" and its no-modem errors name none.
func parseCalls(out string) []string {
	var calls []string
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, callPrefix); i >= 0 {
			calls = append(calls, strings.Fields(line[i:])[0])
		}
	}
	return calls
}

// isNoModem reports mmcli's failure for a box with no modem.
func isNoModem(out string) bool {
	out = strings.ToLower(out)
	return strings.Contains(out, "couldn't find modem") || strings.Contains(out, "no modems were found")
}
