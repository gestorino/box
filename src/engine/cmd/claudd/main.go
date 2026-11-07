// Command claudd runs the owner's agent on Claude Code, signed in on the owner's plan, and serves the
// agent contract to the engine over a Unix socket that systemd hands it (concierge-claudd.socket).
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
	"concierge/engine/internal/agent/claude"
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
	return agent.Serve(ctx, "claudd", claude.NewRunner(*bin, *home, *model), claude.NewLogin(*bin, *home), *listen,
		"version", version, "revision", revision, "model", *model)
}
