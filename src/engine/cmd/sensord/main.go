// Command sensord writes the box's board temperatures, fan modes, package energy and power
// limits, C-state limit and each service's storage to a textfile for node_exporter's textfile
// collector, every 10 s. It never runs as root and writes to hardware only the IT8784's index
// port (0xa45); see infra/bkhd-1264/README.md, journals 13, 14 and 16.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"concierge/engine/internal/sensors"
)

var (
	version  = "dev"
	revision = ""
)

func main() {
	if err := run(); err != nil {
		slog.Error("sensord", "err", err)
		os.Exit(1)
	}
}

func run() error {
	textfile := flag.String("textfile", "", "the full path of concierge-sensors.prom (required)")
	interval := flag.Duration("interval", 10*time.Second, "how often to write the file")
	storageEvery := flag.Duration("storage-every", 5*time.Minute, "how often to re-measure storage")
	storage := flag.String("storage", "", "service=dir,… the directories whose size is reported")
	raplDir := flag.String("rapl-dir", "/sys/class/powercap/intel-rapl:0", "the package's powercap directory")
	flag.Parse()
	if *textfile == "" {
		return errors.New("no textfile: pass -textfile")
	}
	dirs, err := parseStorage(*storage)
	if err != nil {
		return err
	}
	port, err := sensors.OpenPort("/dev/port")
	if err != nil {
		return err
	}
	defer port.Close()
	msr, err := sensors.OpenMSR("/dev/cpu/0/msr")
	if err != nil {
		return err
	}
	defer msr.Close()

	c := &sensors.Collector{Port: port, MSR: msr, RAPLDir: *raplDir, Storage: dirs, StorageEvery: *storageEvery, Now: time.Now}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	slog.Info("sensord writing", "textfile", *textfile, "version", version, "revision", revision)
	for {
		if err := c.WriteTextfile(*textfile); err != nil {
			slog.Warn("sensord: write textfile", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func parseStorage(s string) (map[string]string, error) {
	dirs := map[string]string{}
	if s == "" {
		return dirs, nil
	}
	for _, kv := range strings.Split(s, ",") {
		service, dir, ok := strings.Cut(kv, "=")
		if !ok || service == "" || dir == "" {
			return nil, fmt.Errorf("-storage: %q is not service=dir", kv)
		}
		dirs[service] = dir
	}
	return dirs, nil
}
