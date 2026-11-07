// Command whatsappd links the box as a WhatsApp device for its SIM's account, archives every message,
// sends what the owner sends, and serves them to the engine over a Unix socket that systemd hands it
// (concierge-whatsapp.socket).
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
	waLog "go.mau.fi/whatsmeow/util/log"

	"concierge/engine/internal/h2c"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/whatsapp"
)

// Stamped in at build time, as for the engine.
var (
	version  = "dev"
	revision = ""
)

func main() {
	if err := run(); err != nil {
		slog.Error("whatsappd", "err", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", os.Getenv("STATE_DIRECTORY"), "where session.db and archive.db live")
	socket := flag.String("listen", "", "a Unix socket path to listen on when not socket-activated (development)")
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
	archive, err := whatsapp.OpenArchive(filepath.Join(*dir, "archive.db"))
	if err != nil {
		return err
	}
	defer archive.Close()
	// WARN and up only: whatsmeow's debug logs carry message details.
	client, err := whatsapp.NewMeow(ctx, filepath.Join(*dir, "session.db"), waLog.Stdout("whatsmeow", "WARN", false))
	if err != nil {
		return err
	}
	hub := listen.NewHub(listen.MatchWhatsApp)
	archive.SetHub(hub)
	svc := whatsapp.NewService(client, archive, time.Now)
	if err := svc.Start(ctx); err != nil { // connects in the background, retrying
		return err
	}
	defer client.Disconnect()

	srv := &http.Server{Handler: whatsapp.NewHandler(svc, whatsapp.NewListen(archive, hub)), Protocols: h2c.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("whatsappd listening", "addr", ln.Addr(), "version", version, "revision", revision, "state", svc.Status().State)
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
