package sensors

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PowerLimits reads the package's long- and short-term power limits, in watts, from the
// powercap directory (constraint_0 and constraint_1).
func PowerLimits(raplDir string) (longTerm, shortTerm float64, err error) {
	long, err := readMicrowatts(filepath.Join(raplDir, "constraint_0_power_limit_uw"))
	if err != nil {
		return 0, 0, err
	}
	short, err := readMicrowatts(filepath.Join(raplDir, "constraint_1_power_limit_uw"))
	if err != nil {
		return 0, 0, err
	}
	return long, short, nil
}

func readMicrowatts(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	uw, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return float64(uw) / 1e6, nil
}
