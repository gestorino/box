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

// parseCalls returns the paths of the calls not yet ended in mmcli's --voice-list-calls output, a
// line "<path> <direction> (<state>)" each. An ended call ("terminated") stays listed until
// ModemManager deletes it, and keeps nothing waiting; a line whose state can't be read counts as a
// call. "No calls were found" and its no-modem errors name none.
func parseCalls(out string) []string {
	var calls []string
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, callPrefix)
		if i < 0 {
			continue
		}
		rest := strings.TrimSpace(line[i:])
		if callState(rest) == "terminated" {
			continue
		}
		calls = append(calls, strings.Fields(rest)[0])
	}
	return calls
}

// callState is the state in a call line's trailing parentheses, "" when it has none.
func callState(line string) string {
	open := strings.LastIndex(line, "(")
	if open < 0 || !strings.HasSuffix(line, ")") {
		return ""
	}
	return line[open+1 : len(line)-1]
}

// isNoModem reports mmcli's failure for a box with no modem.
func isNoModem(out string) bool {
	out = strings.ToLower(out)
	return strings.Contains(out, "couldn't find modem") || strings.Contains(out, "no modems were found")
}
