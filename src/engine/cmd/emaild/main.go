// Command emaild archives every email of one mailbox — received and sent, with attachments —
// read-only over IMAP (apart from the read marks, set on a short connection of their own), sends
// the owner's emails through Gmail SMTP, and serves the archive to the engine over a Unix socket
// that systemd hands it (concierge-email.socket).
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

	"concierge/engine/internal/email"
	"concierge/engine/internal/h2c"
	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/secrets"
)

// Stamped in at build time, as for the engine.
var (
	version  = "dev"
	revision = ""
)

func main() {
	if err := run(); err != nil {
		slog.Error("emaild", "err", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", os.Getenv("STATE_DIRECTORY"), "where email.db lives")
	settingsSocket := flag.String("settings-socket", "/run/concierge-hostd/settings.sock", "hostd's read-only settings socket")
	server := flag.String("imap", "imap.gmail.com:993", "the IMAP server (TLS)")
	smtpServer := flag.String("smtp", "smtp.gmail.com:465", "the SMTP server (TLS) the owner's emails go out through")
	secretsSocket := flag.String("secrets-socket", "", "secretd's socket, where the mailbox's app password is kept (required)")
	socket := flag.String("listen", "", "a Unix socket path when not socket-activated (development)")
	flag.Parse()
	if *dir == "" {
		return errors.New("no state directory: run under systemd (StateDirectory) or pass -dir")
	}
	if *secretsSocket == "" {
		return errors.New("no secrets socket: pass -secrets-socket")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := listener(*socket)
	if err != nil {
		return err
	}
	archive, err := email.OpenArchive(filepath.Join(*dir, "email.db"), time.Now)
	if err != nil {
		return err
	}
	defer archive.Close()
	hub := listen.NewHub(listen.MatchEmail)
	archive.SetHub(hub)

	settings := hostsettings.NewClient(*settingsSocket, time.Minute)
	go settings.Run(ctx)
	retention := func() (int, bool) { r, known := settings.Retention(); return r.EmailDays, known }
	svc := email.NewService(email.NewIMAPDial(*server, true), email.NewSMTPSend(*smtpServer, true), email.NewGmailReconcile(*server, true),
		secrets.NewClient(*secretsSocket),
		archive, retention, settings.Uplink, time.Now)
	if err := svc.Start(ctx); err != nil {
		return err
	}

	srv := &http.Server{Handler: email.NewHandler(svc, archive, email.NewListen(svc, archive, hub)), Protocols: h2c.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("emaild listening", "addr", ln.Addr(), "version", version, "revision", revision, "state", svc.Status().State)
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
