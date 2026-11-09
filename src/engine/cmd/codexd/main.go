// Command codexd runs the owner's agent on Codex, signed in on the owner's plan, and serves the
// agent contract to the engine over a Unix socket that systemd hands it (concierge-codex.socket).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"concierge/engine/internal/agent"
	"concierge/engine/internal/agent/codex"
)

// Stamped in at build time, as for the engine.
var (
	version  = "dev"
	revision = ""
)

func main() {
	if err := run(); err != nil {
		slog.Error("codexd", "err", err)
		os.Exit(1)
	}
}

func run() error {
	home := flag.String("home", os.Getenv("STATE_DIRECTORY"), "where the CLI's login (.codex) and working directory live")
	bin := flag.String("codex", "codex", "the Codex binary")
	model := flag.String("model", "gpt-5.5", "the model to run")
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
	return agent.Serve(ctx, "codexd", codex.NewRunner(*bin, *home, *model), codex.NewLogin(*bin, *home), *model, *listen,
		"version", version, "revision", revision)
}
