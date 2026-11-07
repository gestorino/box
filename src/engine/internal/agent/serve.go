package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/coreos/go-systemd/v22/activation"

	"concierge/engine/internal/gen/agent/v1/agentv1connect"
	"concierge/engine/internal/h2c"
)

// Serve serves the agent contract of r and l over HTTP/2 cleartext on the socket systemd hands the
// daemon, or on listenPath when it is not socket-activated (development), until ctx ends. attrs
// are logged with the listening line.
func Serve(ctx context.Context, name string, r Runner, l Login, listenPath string, attrs ...any) error {
	ln, err := listener(listenPath)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewAgentServiceHandler(NewServer(r, l)))
	srv := &http.Server{Handler: mux, Protocols: h2c.Protocols(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info(name+" listening", append([]any{"addr", ln.Addr()}, attrs...)...)
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
