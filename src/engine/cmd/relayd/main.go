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
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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

// The service's domains (both public values): the site and its control plane, and the boxes' names
// and relay.
const (
	defaultControlPlane = "https://gestorino.com"
	defaultBoxDomain    = "gestorino.net"
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
	flag.Parse()
	if *dir == "" {
		return errors.New("no state directory: run under systemd (StateDirectory) or pass -dir")
	}
	if *secretsSocket == "" {
		return errors.New("no secrets socket: pass -secrets-socket")
	}
	if err := checkDomains(*controlPlane, *boxDomain); err != nil {
		return err
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
	cert, err := relay.NewCert(*dir, *front)
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

// checkDomains refuses a control plane that isn't an https:// origin (the box's secret goes to it)
// and a box domain that isn't a lowercase hostname without a trailing dot (a claim must name it
// exactly). The Cloud client takes any base, so tests can point it at a loopback server.
func checkDomains(controlPlane, boxDomain string) error {
	u, err := url.Parse(controlPlane)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("-control-plane %q: want an https:// origin", controlPlane)
	}
	if !isHostname(boxDomain) {
		return fmt.Errorf("-box-domain %q: want a lowercase hostname without a trailing dot", boxDomain)
	}
	return nil
}

// isHostname says name is two or more DNS labels of a-z, 0-9 and inner hyphens.
func isHostname(name string) bool {
	labels := strings.Split(name, ".")
	if len(name) > 253 || len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, r := range l {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
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
