// Command engine is the concierge's server: the web API, the browser-use worker, and the
// communication channels.
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

	"concierge/engine/internal/aikey"
	"concierge/engine/internal/api"
	"concierge/engine/internal/browser"
	"concierge/engine/internal/channels"
	"concierge/engine/internal/health"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/secrets"
	"concierge/engine/internal/signins"
)

// Stamped in at build time: go build -ldflags "-X main.version=0.1.0 -X main.revision=abc1234".
var (
	version  = "dev"
	revision = ""
)

func main() {
	if err := run(); err != nil {
		slog.Error("engine", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	workerSocket := flag.String("worker-socket", "", "the browser worker's Unix socket; empty disables the browser")
	secretsSocket := flag.String("secrets-socket", "", "secretd's Unix socket: the OpenRouter key and the accounts' passwords (required)")
	screensDir := flag.String("screens-dir", "", "the worker's screens, one directory per site (read only here); empty serves no screens")
	recordingsDir := flag.String("recordings-dir", "", "the worker's videos, <dir>/<site>/<run>/<n>.mp4 (read only here, needs -screens-dir); empty plays none")
	noVNCDir := flag.String("novnc-dir", "/usr/share/novnc", "noVNC's files (the novnc package), served for the screens under -screens-dir")
	uiDir := flag.String("ui-dir", "", "serve the built UI (src/ui/dist) at /; empty serves the API only")
	hostdSocket := flag.String("hostd-socket", "", "hostd's Unix socket; empty leaves /api/v1/network answering 503")
	whatsappSocket := flag.String("whatsapp-socket", "", "whatsappd's Unix socket; empty leaves /api/v1/whatsapp answering 503")
	emailSocket := flag.String("email-socket", "", "emaild's Unix socket; empty leaves /api/v1/email answering 503")
	telegramSocket := flag.String("telegram-socket", "", "telegramd's Unix socket; empty leaves /api/v1/telegram answering 503")
	smsSocket := flag.String("sms-socket", "", "smsd's Unix socket; empty leaves /api/v1/sms answering 503")
	callsSocket := flag.String("calls-socket", "", "calld's Unix socket; empty leaves /api/v1/calls answering 503")
	signinsDB := flag.String("signins-db", "", "the sign-ins catalog database; empty leaves /api/v1/signins answering 503")
	prometheusURL := flag.String("prometheus-url", "", "Prometheus's base URL, where the Health page reads its history; empty leaves /api/v1/health answering 503")
	claudeSocket := flag.String("claude-socket", "", "the Claude agent daemon's Unix socket; empty leaves that agent out")
	codexSocket := flag.String("codex-socket", "", "the Codex agent daemon's Unix socket; empty leaves that agent out")
	agentDB := flag.String("agent-db", "", "the owner's agent conversations database; empty runs no agent and no WhatsApp listener")
	timeZone := flag.String("time-zone", localZone(), "the owner's time zone, told to the agent")
	consoleURL := flag.String("console-url", "", "the console's address, for the links the agent sends (empty: the path alone)")
	metricsAddr := flag.String("metrics-addr", "", "serve the running runs to Prometheus at /metrics on this address, with no login (keep it on 127.0.0.1); empty serves none")
	login := api.Login{User: os.Getenv("CONCIERGE_CONSOLE_USER"), PasswordHash: os.Getenv("CONCIERGE_CONSOLE_PASSWORD_HASH")}
	flag.Parse()
	// No login, no console: the engine never falls back to serving without one.
	if err := login.Validate(); err != nil {
		return err
	}
	sc, err := secretsClient(*secretsSocket)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg, err := channels.NewRegistry()
	if err != nil {
		return err
	}

	// The sign-ins service needs a worker even when the browser is disabled: an unstarted one answers
	// ErrUnavailable (503), while its reads keep working.
	w := browser.New()
	signInsHandler, runsHandler := api.SocketProxy("", "accounts"), api.SocketProxy("", "runs")
	var signInsSvc *signins.Service
	events := &eventForwarder{}
	var running health.RunLister
	var runStore health.RunStore // nil without a sign-ins database: Health's bands go unlabelled
	if *signinsDB != "" {
		svc, store, err := openSignIns(ctx, *signinsDB, w, sc, *screensDir, *recordingsDir, *noVNCDir,
			*smsSocket, *emailSocket, *hostdSocket, events.Event)
		if err != nil {
			return err
		}
		defer store.Close()
		running, runStore = store, store
		signInsSvc, signInsHandler, runsHandler = svc, signins.NewHandler(svc), signins.NewRunsHandler(svc)
	}

	// An interface holding a nil *Worker is not nil, so the disabled case passes a literal nil.
	var b api.Browser
	if *workerSocket != "" {
		b = w
		// The worker is its own service: the engine dials it and re-dials, and a worker that goes away
		// only makes browser requests unavailable until it is back.
		go w.Run(ctx, *workerSocket, func(ctx context.Context) (string, error) { return aikey.Key(ctx, sc) })
	}

	var healthHandler http.Handler
	if *prometheusURL != "" {
		q, err := health.NewQuerier(*prometheusURL)
		if err != nil {
			return fmt.Errorf("-prometheus-url: %w", err)
		}
		var calls health.CallLister
		if *callsSocket != "" {
			calls = health.NewCallLister(*callsSocket)
		}
		healthHandler = health.Handler(q, runStore, calls)
	}

	// The hub exists, and the forwarder has it, before the server or any run can start.
	agentsHandler, waitConverse, err := startConverse(ctx, converseConfig{DB: *agentDB, ClaudeSocket: *claudeSocket, CodexSocket: *codexSocket,
		TimeZone: *timeZone, ConsoleURL: *consoleURL, WhatsAppSocket: *whatsappSocket, HostdSocket: *hostdSocket}, signInsSvc, events)
	if err != nil {
		return fmt.Errorf("-agent-db: %w", err)
	}

	srv := newServer(ctx, *addr, api.New(api.Config{
		Login:    login,
		Build:    api.Build{Version: version, Revision: revision},
		Browser:  b,
		Channels: reg,
		UIDir:    *uiDir,
		Network:  api.SocketProxy(*hostdSocket, "host service"),
		SMS:      api.SocketProxy(*smsSocket, "SMS service"),
		Calls:    api.SocketProxy(*callsSocket, "call service"),
		WhatsApp: api.SocketProxy(*whatsappSocket, "WhatsApp service"),
		Email:    api.SocketProxy(*emailSocket, "email service"),
		Telegram: api.SocketProxy(*telegramSocket, "Telegram service"),
		SignIns:  signInsHandler,
		Accounts: signInsHandler,
		Runs:     runsHandler,
		Health:   healthHandler,
		AI:       aikey.NewHandler(sc, aikey.NewChecker()),
		Agents:   agentsHandler,
	}))
	// The activity endpoint is a second listener, so the console's login stays on every request of the
	// first. Without a sign-ins database there are no runs to report, so it serves none.
	var metricsSrv *http.Server
	if *metricsAddr != "" && running != nil {
		metricsSrv = newServer(ctx, *metricsAddr, health.ActivityHandler(running))
		go func() {
			if err := metricsSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				slog.Error("metrics listener", "addr", *metricsAddr, "err", err)
			}
		}()
	}
	// Shutting down: stop serving, then give the sign-ins runs (cancelled with ctx) what is left of
	// the window to record where they stopped, before the store closes.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
		if metricsSrv != nil {
			metricsSrv.Shutdown(shutdown)
		}
		waitConverse()
		if signInsSvc != nil {
			idle := make(chan struct{})
			go func() { signInsSvc.Wait(); close(idle) }()
			select {
			case <-idle:
			case <-shutdown.Done():
				slog.Warn("sign-ins runs still going at shutdown")
			}
		}
	}()
	slog.Info("engine listening", "addr", *addr, "version", version, "revision", revision)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		// Not a shutdown: stop what runs under ctx, and wait for the hub and its listener.
		stop()
		waitConverse()
		return err
	}
	<-drained
	return nil
}

// newServer serves h on addr, every request under ctx: a request that runs until its client goes
// (the live view's MJPEG stream) ends when the engine stops, rather than holding Shutdown to its
// deadline.
func newServer(ctx context.Context, addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
}

// secretsClient is the engine's way to secretd, which keeps every secret it uses; the engine refuses
// to start without one.
func secretsClient(socket string) (*secrets.Client, error) {
	if socket == "" {
		return nil, errors.New("-secrets-socket is required: secretd keeps the engine's secrets")
	}
	return secrets.NewClient(socket), nil
}

// hasKey is every run's gate: whether the AI page's OpenRouter key is in secretd.
func hasKey(sc *secrets.Client) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		k, err := aikey.Key(ctx, sc)
		return k != "", err
	}
}

// openSignIns opens the store and builds the service; the catalog is the store's own, grown through the
// API. The accounts' passwords and the OpenRouter key that gates every run are in secretd (sc). base is
// what background runs live under.
func openSignIns(base context.Context, db string, worker signins.Runner, sc *secrets.Client, screensDir, recordingsDir, noVNCDir string,
	smsSocket, emailSocket, hostdSocket string, events func(signins.RunEvent)) (*signins.Service, *signins.Store, error) {
	store, err := signins.OpenStore(db)
	if err != nil {
		return nil, nil, fmt.Errorf("-signins-db: %w", err)
	}
	hostd := signins.NewHostdClient(hostdSocket)
	return signins.NewService(signins.Config{
		Store: store, Secrets: sc, Worker: worker, ScreensDir: screensDir, RecordingsDir: recordingsDir, NoVNCDir: noVNCDir,
		Base:   base,
		SMS:    listen.NewClient(smsSocket),
		Mail:   signins.NewMailClient(emailSocket),
		Inbox:  listen.NewClient(emailSocket),
		Uplink: hostd,
		Owner:  hostd,
		HasKey: hasKey(sc),
		Events: events,
	}), store, nil
}
