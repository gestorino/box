package sensors

import "fmt"

// MSR reads model-specific registers (/dev/cpu/0/msr).
type MSR interface {
	Read(reg int64) (uint64, error)
}

// PackageEnergyJoules is the package's energy counter in joules: MSR 0x611's low 32 bits times
// the unit 1/2^((MSR 0x606 >> 8) & 0x1F). The counter wraps; report it as a Prometheus counter.
func PackageEnergyJoules(m MSR) (float64, error) {
	units, err := m.Read(0x606)
	if err != nil {
		return 0, fmt.Errorf("msr 0x606: %w", err)
	}
	raw, err := m.Read(0x611)
	if err != nil {
		return 0, fmt.Errorf("msr 0x611: %w", err)
	}
	unit := 1 / float64(uint64(1)<<((units>>8)&0x1F))
	return float64(raw&0xFFFFFFFF) * unit, nil
}

// CStateLimit is MSR 0xE2's package C-state limit (bits 0-3) and its lock (bit 15).
func CStateLimit(m MSR) (limit int, locked bool, err error) {
	v, err := m.Read(0xE2)
	if err != nil {
		return 0, false, fmt.Errorf("msr 0xe2: %w", err)
	}
	return int(v & 0xF), v&(1<<15) != 0, nil
}
