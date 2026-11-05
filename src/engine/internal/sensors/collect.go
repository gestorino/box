package sensors

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Collector gathers every reading and writes them as one textfile. A source that fails is left
// out of the file (and logged once per failure streak); it never fails the whole file.
type Collector struct {
	Port         Port
	MSR          MSR
	RAPLDir      string
	Storage      map[string]string // service -> directory
	StorageEvery time.Duration
	Now          func() time.Time

	dirBytes    func(string) (int64, error) // DirBytes; replaced in tests
	storageAt   time.Time
	storageLast map[string]float64
	failing     map[string]bool
}

// note logs a source's failure once per streak and clears the streak on success.
func (c *Collector) note(source string, err error) bool {
	if c.failing == nil {
		c.failing = map[string]bool{}
	}
	if err == nil {
		c.failing[source] = false
		return true
	}
	if !c.failing[source] {
		slog.Warn("sensord: source failed", "source", source, "err", err)
	}
	c.failing[source] = true
	return false
}

func (c *Collector) measureStorage() {
	now := c.Now()
	if c.storageLast != nil && now.Sub(c.storageAt) < c.StorageEvery {
		return
	}
	c.storageAt = now
	measure := c.dirBytes
	if measure == nil {
		measure = DirBytes
	}
	last := map[string]float64{}
	for service, dir := range c.Storage {
		n, err := measure(dir)
		if c.note("storage "+service, err) {
			last[service] = float64(n)
		}
	}
	c.storageLast = last
}

// WriteTextfile writes the readings to path, atomically (prometheus.WriteToTextfile).
func (c *Collector) WriteTextfile(path string) error {
	reg := prometheus.NewRegistry()
	gauge := func(name, help string, labels ...string) *prometheus.GaugeVec {
		g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
		reg.MustRegister(g)
		return g
	}

	if temps, err := BoardTemps(c.Port); c.note("board temps", err) {
		g := gauge("concierge_board_temp_celsius", "The board's temperature, by sensor.", "sensor")
		for i, t := range temps {
			g.WithLabelValues(strconv.Itoa(i + 1)).Set(t)
		}
	}
	if fans, err := FanOutputs(c.Port); c.note("fan outputs", err) {
		auto := gauge("concierge_fan_output_automatic", "1 when the fan output is automatic, 0 when manual.", "output")
		pwm := gauge("concierge_fan_output_manual_pwm_ratio", "The fan output's manual PWM duty, 0 to 1.", "output")
		for _, f := range fans {
			o := strconv.Itoa(f.Output)
			a := 0.0
			if f.Automatic {
				a = 1
			}
			auto.WithLabelValues(o).Set(a)
			pwm.WithLabelValues(o).Set(f.ManualPWM)
		}
	}
	if j, err := PackageEnergyJoules(c.MSR); c.note("package energy", err) {
		reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "concierge_package_energy_joules_total",
			Help: "The package's energy counter, in joules (32 bits: it wraps).",
		}, func() float64 { return j }))
	}
	if limit, locked, err := CStateLimit(c.MSR); c.note("c-state limit", err) {
		gauge("concierge_package_cstate_limit", "The package C-state limit (MSR 0xE2).").WithLabelValues().Set(float64(limit))
		l := 0.0
		if locked {
			l = 1
		}
		gauge("concierge_package_cstate_locked", "1 when the package C-state limit is locked.").WithLabelValues().Set(l)
	}
	if long, short, err := PowerLimits(c.RAPLDir); c.note("power limits", err) {
		g := gauge("concierge_power_limit_watts", "The package's power limits.", "limit")
		g.WithLabelValues("long_term").Set(long)
		g.WithLabelValues("short_term").Set(short)
	}
	c.measureStorage()
	if len(c.storageLast) > 0 {
		g := gauge("concierge_storage_bytes", "Disk used by a service's data, by service.", "service")
		for service, n := range c.storageLast {
			g.WithLabelValues(service).Set(n)
		}
	}
	return prometheus.WriteToTextfile(path, reg)
}
