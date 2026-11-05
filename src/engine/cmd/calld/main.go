// Command calld follows every call on the modem — incoming and outgoing — records its times and
// outcome in its own database, clears it from ModemManager, purges by the retention hostd holds,
// places the owner's prompt calls (OpenAI's voice model, on the model the owner chose, holds the
// conversation; the setup check talks through it too), keeps a held call's summary — and its
// transcript or audio when hostd's settings say so — and serves the history to the engine over a
// Unix socket that systemd hands it (concierge-calls.socket). It keeps the owner's OpenAI key, set in
// Voice's setup, in secretd (checked with OpenAI before it is kept, read fresh for every call,
// forgotten when Voice is removed); no key, no call, and no secretd, no call.
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

	"concierge/engine/internal/calls"
	"concierge/engine/internal/h2c"
	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/realtime"
	"concierge/engine/internal/secrets"
)

var (
	version  = "dev"
	revision = ""
)

func main() {
	if err := run(); err != nil {
		slog.Error("calld", "err", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", os.Getenv("STATE_DIRECTORY"), "where calls.db lives")
	secretsSocket := flag.String("secrets-socket", "", "secretd's socket, where the owner's OpenAI key is kept (required)")
	settingsSocket := flag.String("settings-socket", "/run/concierge-hostd/settings.sock", "hostd's read-only settings socket")
	listen := flag.String("listen", "", "a Unix socket path when not socket-activated (development)")
	textfileDir := flag.String("textfile-dir", "", "where node_exporter's textfile collector reads concierge-modem.prom (empty: off)")
	flag.Parse()
	if *secretsSocket == "" {
		return errors.New("no secrets socket: pass -secrets-socket")
	}
	if *dir == "" {
		return errors.New("no state directory: run under systemd (StateDirectory) or pass -dir")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := listener(*listen)
	if err != nil {
		return err
	}
	archive, err := calls.OpenArchive(filepath.Join(*dir, "calls.db"), time.Now)
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
	modem := calls.NewDBusModem(bus)
	archiver := calls.NewArchiver(modem, archive,
		func() (int, bool) { r, known := settings.Retention(); return r.CallsDays, known },
		func() (int, bool) { k, known := settings.CallsKeep(); return k.DebugDays, known }, time.Now)
	go archiver.Run(ctx, 2*time.Second)

	// The voice model for the prompt call and the check, on the owner's OpenAI key, set in Voice's
	// setup and kept in secretd; each call opens with the key stored as it starts. The model
	// is the session's, from the owner's Voice settings.
	ai := calls.OpenAI{
		Keys:    secrets.NewClient(*secretsSocket),
		Checker: calls.NewKeyChecker(),
		Open: func(ctx context.Context, key string, spec calls.SessionSpec) (calls.Talker, error) {
			s, err := realtime.Open(ctx, realtime.Config{
				URL: "wss://api.openai.com/v1/realtime", Key: key, Model: spec.Model, Transcribe: "gpt-4o-transcribe",
				Language: spec.Language, Lead: spec.Lead, Tools: spec.Tools,
			}, spec.Instructions)
			if err != nil {
				return nil, err // never a nil *Session inside a Talker
			}
			return s, nil
		},
	}
	// A call's recording is written here and renamed into the archive once its row exists: the same
	// filesystem as calls.db, owner-only. Whatever is left from a run that stopped mid-call has no row
	// to go to.
	tmp := filepath.Join(*dir, "tmp")
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return err
	}
	// A call reads the keep switches as it starts, fresh from hostd, so a switch the owner just turned
	// off keeps nothing from the next call; if hostd doesn't answer in time, the last value seen stands.
	callsKeep := func() (hostsettings.CallsKeep, bool) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		settings.Refresh(ctx)
		return settings.CallsKeep()
	}
	// A call takes the owner's Voice settings, and the names from Account, the same way: fresh as it starts, else the last seen.
	voice := func() (hostsettings.Voice, bool) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		settings.Refresh(ctx)
		return settings.Voice()
	}
	owner := func() (hostsettings.Owner, bool) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		settings.Refresh(ctx)
		return settings.Owner()
	}
	if *textfileDir != "" {
		metrics := &calls.MetricsWriter{Dir: *textfileDir, Live: archive.Live, Now: time.Now,
			QTemp: calls.ModemTemperature}
		go metrics.Run(ctx)
	}
	caller := calls.NewCaller(modem, calls.NewAudio(), calls.NewDuplex(), archiver.Hold, archiver.Keep, callsKeep, tmp, 45*time.Second, 500*time.Millisecond)
	srv := &http.Server{Handler: calls.NewHandler(ctx, archive, caller, ai, voice, owner), Protocols: h2c.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("calld listening", "addr", ln.Addr(), "version", version, "revision", revision)
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
