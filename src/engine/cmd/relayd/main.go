// Command relayd claims the box to its owner's account and holds it on the relay, so the owner's
// devices reach the console from the internet: frp's client to the relay, a TLS front on loopback
// that lets in only the box's device certificates, and the front's certificate from Let's
// Encrypt. It serves the console the claim and the devices over a Unix socket that systemd hands
// it (concierge-relay.socket). Design: docs/superpowers/specs/2026-11-08-remote-access-box-design.md.
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
	"concierge/engine/internal/relay"
	"concierge/engine/internal/secrets"
)

// Stamped in at build time, as for the engine.
var (
	version  = "dev"
	revision = ""
)

// The service's domains, placeholders until the owner's exist (both public values).
const (
	defaultControlPlane = "https://example-site.invalid"
	defaultBoxDomain    = "example-box.invalid"
)

func main() {
	if err := run(); err != nil {
		slog.Error("relayd", "err", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", os.Getenv("STATE_DIRECTORY"), "where relay.db and the certificate live (absolute)")
	listen := flag.String("listen", "", "a Unix socket path to listen on when not socket-activated (development)")
	secretsSocket := flag.String("secrets-socket", "", "secretd's socket, where the box secret and the device CA are kept (required)")
	controlPlane := flag.String("control-plane", defaultControlPlane, "the control plane's base URL")
	boxDomain := flag.String("box-domain", defaultBoxDomain, "the domain the box's name and relay are under; a claim naming another is refused")
	engine := flag.String("engine", "127.0.0.1:80", "the engine's address, where the front forwards the devices' requests")
	front := flag.String("front", "127.0.0.1:8443", "the TLS front's address: the relay's proxy target and the ACME check's")
	caFile := flag.String("ca-file", "/etc/ssl/certs/ca-certificates.crt", "the roots the relay's certificate is checked against")
	acmeEmail := flag.String("acme-email", "", "the ACME account's contact (default: the account's email)")
	flag.Parse()
	if *dir == "" {
		return errors.New("no state directory: run under systemd (StateDirectory) or pass -dir")
	}
	if *secretsSocket == "" {
		return errors.New("no secrets socket: pass -secrets-socket")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := listener(*listen)
	if err != nil {
		return err
	}
	store, err := relay.OpenStore(filepath.Join(*dir, "relay.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	sec := secrets.NewClient(*secretsSocket)
	ca, err := relay.LoadCA(ctx, sec, time.Now) // its one caller
	if err != nil {
		return fmt.Errorf("device CA: %w", err)
	}
	cert, err := relay.NewCert(*dir, *acmeEmail, *front)
	if err != nil {
		return err
	}

	// The front listens before anything can Manage the certificate: certmagic, finding the port
	// held, leaves the ACME check to the front instead of binding the port itself.
	fln, err := net.Listen("tcp", *front)
	if err != nil {
		return err
	}
	fr := relay.NewFront(ca, func(serial string) bool { return store.ActiveSerial(serial, time.Now()) },
		cert.ServerCert, cert.ALPNChallenge, *engine)
	frontErr := make(chan error, 1)
	go func() {
		if err := fr.Serve(fln); !errors.Is(err, http.ErrServerClosed) {
			frontErr <- fmt.Errorf("the front stopped: %w", err)
			stop()
		}
	}()

	tunnel := relay.NewFrpTunnel(*caFile, *front, cert)
	svc := relay.NewService(store, relay.NewCloud(*controlPlane, version, *boxDomain), sec, tunnel, tunnel.Waiting, time.Now)
	ran := make(chan struct{})
	go func() { svc.Run(ctx); close(ran) }() // stops the tunnel when ctx ends

	srv := &http.Server{Handler: relay.NewHandler(svc, ca, fr.CloseDevice), Protocols: h2c.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("relayd listening", "addr", ln.Addr(), "front", fln.Addr(), "version", version, "revision", revision,
		"state", svc.Status().State)
	err = srv.Serve(ln)
	stop()
	<-ran
	// The front's requests get a short grace; its hijacked WebSockets (live views) are neither
	// drained by Shutdown nor closed by Close, and end with the process.
	grace, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	fr.Shutdown(grace)
	fr.Close()
	select {
	case ferr := <-frontErr:
		return ferr
	default:
	}
	if !errors.Is(err, http.ErrServerClosed) {
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
