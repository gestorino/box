// Command hostd is the host service: the only process that changes the box's network, and the one
// that starts and stops the connectors (only added ones run). It runs as its own user, holds exactly
// the permissions polkit grants it, and serves the engine over a Unix socket that systemd hands it
// (concierge-hostd.socket).
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
	"concierge/engine/internal/hostd"
	"concierge/engine/internal/hostd/sysbus"
	"concierge/engine/internal/secrets"
)

// Stamped in at build time, as for the engine.
var (
	version  = "dev"
	revision = ""
)

func main() {
	if err := run(); err != nil {
		slog.Error("hostd", "err", err)
		os.Exit(1)
	}
}

func run() error {
	wifi := flag.String("wifi-iface", "wlo1", "the Wi-Fi interface")
	modemConn := flag.String("modem-conn", "meo-lte", "the NetworkManager connection for mobile data")
	dir := flag.String("dir", os.Getenv("STATE_DIRECTORY"), "where settings.db lives")
	listen := flag.String("listen", "", "API socket path when not socket-activated (development)")
	settingsListen := flag.String("settings-listen", "", "read-only settings socket path when not socket-activated (development)")
	secretsSocket := flag.String("secrets-socket", "", "secretd's socket, where the owner's card number is kept (required)")
	flag.Parse()
	if *secretsSocket == "" {
		return errors.New("no secrets socket: pass -secrets-socket")
	}
	if *dir == "" {
		return errors.New("no state directory: run under systemd (StateDirectory) or pass -dir")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	api, settingsLn, err := listeners(*listen, *settingsListen)
	if err != nil {
		return err
	}
	settings, err := hostd.OpenSettings(filepath.Join(*dir, "settings.db"), secrets.NewClient(*secretsSocket))
	if err != nil {
		return err
	}
	defer settings.Close()
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer bus.Close()

	units, err := sysbus.NewUnits(ctx)
	if err != nil {
		return err
	}
	svc := hostd.NewService(sysbus.NewNM(bus, *wifi, *modemConn), sysbus.NewMM(bus), units, settings, time.Now)
	go svc.Reconcile(ctx) // only added connectors run; serving never waits on systemd
	servers := []*http.Server{
		{Handler: hostd.NewHandler(svc, settings), Protocols: h2c.Protocols(), ReadHeaderTimeout: 10 * time.Second},
		{Handler: hostd.NewSettingsHandler(settings, svc), Protocols: h2c.Protocols(), ReadHeaderTimeout: 10 * time.Second},
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, srv := range servers {
			srv.Shutdown(shutdown)
		}
	}()
	slog.Info("hostd listening", "api", api.Addr(), "settings", settingsLn.Addr(), "version", version, "revision", revision)
	errs := make(chan error, 2)
	go func() { errs <- servers[1].Serve(settingsLn) }()
	go func() { errs <- servers[0].Serve(api) }()
	if err := <-errs; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// listeners are the two sockets systemd passes, named by FileDescriptorName ("api", "settings"), or
// the -listen paths in development.
func listeners(apiPath, settingsPath string) (api, settings net.Listener, err error) {
	named, err := activation.ListenersWithNames()
	if err != nil {
		return nil, nil, err
	}
	if len(named["api"]) == 1 && len(named["settings"]) == 1 {
		return named["api"][0], named["settings"][0], nil
	}
	if apiPath == "" || settingsPath == "" {
		return nil, nil, fmt.Errorf("not socket-activated (want fds named api and settings) and no -listen/-settings-listen paths given")
	}
	if api, err = net.Listen("unix", apiPath); err != nil {
		return nil, nil, err
	}
	if settings, err = net.Listen("unix", settingsPath); err != nil {
		return nil, nil, err
	}
	return api, settings, nil
}
