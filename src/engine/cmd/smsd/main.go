// Command smsd stores every SMS the modem holds — received and sent — in its own database, clears
// it from the modem once stored, purges by the retention hostd holds, and serves the archive to the
// engine over a Unix socket that systemd hands it (concierge-sms.socket).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/activation"
	"github.com/godbus/dbus/v5"

	"concierge/engine/internal/h2c"
	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/sms"
)

var (
	version  = "dev"
	revision = ""
)

func main() {
	if err := run(); err != nil {
		slog.Error("smsd", "err", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", os.Getenv("STATE_DIRECTORY"), "where sms.db lives")
	settingsSocket := flag.String("settings-socket", "/run/concierge-hostd/settings.sock", "hostd's read-only settings socket")
	socket := flag.String("listen", "", "a Unix socket path when not socket-activated (development)")
	flag.Parse()
	if *dir == "" {
		return errors.New("no state directory: run under systemd (StateDirectory) or pass -dir")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := listener(*socket)
	if err != nil {
		return err
	}
	archive, err := sms.OpenArchive(filepath.Join(*dir, "sms.db"), time.Now)
	if err != nil {
		return err
	}
	defer archive.Close()
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer bus.Close()

	settings := hostsettings.NewClient(*settingsSocket, time.Minute)
	go settings.Run(ctx)
	modem := sms.NewDBusModem(bus)
	archiver := sms.NewArchiver(modem, archive, func() (int, bool) { r, known := settings.Retention(); return r.SMSDays, known }, time.Now)
	go archiver.Run(ctx, 10*time.Second)

	hub := listen.NewHub(listen.MatchSMS)
	archive.SetHub(hub)
	srv := &http.Server{Handler: sms.NewHandler(archive, modem, archiver.Tick, sms.NewListen(archive, hub)), Protocols: h2c.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("smsd listening", "addr", ln.Addr(), "version", version, "revision", revision)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func listener(path string) (net.Listener, error) {
	lns, err := activation.Listeners()
	if err != nil {
		return nil, err
	}
	if len(lns) == 1 {
		return lns[0], nil
	}
	if path == "" {
		return nil, fmt.Errorf("not socket-activated and no -listen path given")
	}
	return net.Listen("unix", path)
}
