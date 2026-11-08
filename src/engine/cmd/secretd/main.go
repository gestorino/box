// Command secretd keeps the box's secrets — sealed in one store, one namespace per caller — and
// serves them over a Unix socket that systemd hands it (concierge-secrets.socket). Who is asking is
// the kernel's answer (the socket's peer uid), never the request's; a uid that is no caller is
// refused. Its master key is 32 raw bytes from a file (-master-key-file). It never logs a value.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/activation"

	"concierge/engine/internal/h2c"
	"concierge/engine/internal/secrets"
)

var (
	version  = "dev"
	revision = ""
)

// namespaces are the only callers there are: a typo in a name would silently make a new namespace.
var namespaces = []string{"engine", "calld", "hostd", "emaild", "relay"}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("secretd", "err", err)
		os.Exit(1)
	}
}

// callerFlags collects the repeatable -caller name=user.
type callerFlags []string

func (c *callerFlags) String() string     { return strings.Join(*c, ",") }
func (c *callerFlags) Set(v string) error { *c = append(*c, v); return nil }

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("secretd", flag.ContinueOnError)
	dir := fs.String("dir", os.Getenv("STATE_DIRECTORY"), "where secrets.db lives")
	keyFile := fs.String("master-key-file", "", "the master key: a raw file of exactly 32 bytes")
	listen := fs.String("listen", "", "a Unix socket path when not socket-activated (development)")
	var callerArgs callerFlags
	fs.Var(&callerArgs, "caller", "name=user: the system user that is that namespace's caller (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New("no state directory: run under systemd (StateDirectory) or pass -dir")
	}
	callers, err := resolveCallers(callerArgs)
	if err != nil {
		return err
	}
	key, err := readKey(*keyFile)
	if err != nil {
		return err
	}
	st, err := secrets.OpenStore(filepath.Join(*dir, "secrets.db"), key)
	if err != nil {
		return err
	}
	defer st.Close()
	ln, err := listener(*listen)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler:           secrets.NewHandler(st, callers),
		ConnContext:       secrets.ConnContext,
		Protocols:         h2c.Protocols(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("secretd listening", "addr", ln.Addr(), "version", version, "revision", revision)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// resolveCallers turns name=user flags into the uid → namespace map.
func resolveCallers(flags []string) (map[uint32]string, error) {
	if len(flags) == 0 {
		return nil, errors.New("no callers: pass -caller name=user")
	}
	callers := map[uint32]string{}
	for _, f := range flags {
		name, login, ok := strings.Cut(f, "=")
		if !ok || name == "" || login == "" {
			return nil, fmt.Errorf("-caller %q: want name=user", f)
		}
		if !slices.Contains(namespaces, name) {
			return nil, fmt.Errorf("-caller %q: the namespaces are %s", name, strings.Join(namespaces, ", "))
		}
		u, err := user.Lookup(login)
		if err != nil {
			return nil, fmt.Errorf("-caller %s: user %q: %w", name, login, err)
		}
		uid, err := strconv.ParseUint(u.Uid, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("-caller %s: user %q has uid %q", name, login, u.Uid)
		}
		if uid == 0 {
			return nil, fmt.Errorf("-caller %s: user %q is uid 0, and root is never a caller", name, login)
		}
		if other, dup := callers[uint32(uid)]; dup {
			return nil, fmt.Errorf("-caller %s: user %q is already %s", name, login, other)
		}
		callers[uint32(uid)] = name
	}
	return callers, nil
}

// readKey reads the master key: exactly 32 bytes, never named in an error.
func readKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("no master key: pass -master-key-file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("master key file: %w", err)
	}
	defer f.Close()
	key, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil {
		return nil, fmt.Errorf("master key file: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("master key file %s must hold exactly 32 bytes", path)
	}
	return key, nil
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
		return nil, errors.New("not socket-activated and no -listen path given")
	}
	os.Remove(path) // development: a socket left by the last run
	return net.Listen("unix", path)
}
