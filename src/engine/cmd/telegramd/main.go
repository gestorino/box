// Command telegramd logs the box in as a second device of the Telegram account on its SIM number,
// archives the account's chats, sends what the owner sends, and serves them to the engine over a
// Unix socket that systemd hands it (concierge-telegram.socket).
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

	"concierge/engine/internal/h2c"
	"concierge/engine/internal/telegram"
)

// Stamped in at build time, as for the engine.
var (
	version  = "dev"
	revision = ""
)

func main() {
	if err := run(); err != nil {
		slog.Error("telegramd", "err", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", os.Getenv("STATE_DIRECTORY"), "where telegram.db lives")
	listen := flag.String("listen", "", "a Unix socket path to listen on when not socket-activated (development)")
	flag.Parse()
	if *dir == "" {
		return errors.New("no state directory: run under systemd (StateDirectory) or pass -dir")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := listener(*listen)
	if err != nil {
		return err
	}
	store, err := telegram.OpenStore(filepath.Join(*dir, "telegram.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	ss, us := telegram.NewSessionStore(store), telegram.NewUpdateState(store)
	svc := telegram.NewService(telegram.NewGotd(ss, us, version), store, ss, us)
	if err := svc.Start(ctx); err != nil { // connects in the background, retrying
		return err
	}
	defer svc.Stop()

	srv := &http.Server{Handler: telegram.NewHandler(svc), Protocols: h2c.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("telegramd listening", "addr", ln.Addr(), "version", version, "revision", revision, "state", svc.Status().State)
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
