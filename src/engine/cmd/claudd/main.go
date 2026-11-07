// Command claudd runs the owner's agent on Claude Code, signed in on the owner's plan, and serves the
// agent contract to the engine over a Unix socket that systemd hands it (concierge-claudd.socket).
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
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/activation"

	"concierge/engine/internal/agent"
	"concierge/engine/internal/agent/claude"
	"concierge/engine/internal/gen/agent/v1/agentv1connect"
	"concierge/engine/internal/h2c"
)

// Stamped in at build time, as for the engine.
var (
	version  = "dev"
	revision = ""
)

func main() {
	if err := run(); err != nil {
		slog.Error("claudd", "err", err)
		os.Exit(1)
	}
}

func run() error {
	home := flag.String("home", os.Getenv("STATE_DIRECTORY"), "where the CLI's token and working directory live")
	bin := flag.String("claude", "claude", "the Claude Code binary")
	model := flag.String("model", "sonnet", "the model to run")
	listen := flag.String("listen", "", "a Unix socket path to listen on when not socket-activated (development)")
	flag.Parse()
	if *home == "" {
		return errors.New("no home: run under systemd (StateDirectory) or pass -home")
	}
	if err := os.MkdirAll(*home, 0o700); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := listener(*listen)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewAgentServiceHandler(agent.NewServer(claude.NewRunner(*bin, *home, *model), claude.NewLogin(*bin, *home))))
	srv := &http.Server{Handler: mux, Protocols: h2c.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("claudd listening", "addr", ln.Addr(), "version", version, "revision", revision, "model", *model)
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
