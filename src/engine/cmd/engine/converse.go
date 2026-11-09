package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"concierge/engine/internal/converse"
	"concierge/engine/internal/gen/agent/v1/agentv1connect"
	"concierge/engine/internal/h2c"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/signins"
)

// eventForwarder is signins' Config.Events before the hub exists: the hub needs tools over the
// service, and the service needs somewhere to tell its events. Until the hub is set, an event is
// dropped with a log line.
type eventForwarder struct {
	mu  sync.Mutex
	hub *converse.Hub
}

func (f *eventForwarder) set(h *converse.Hub) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hub = h
}

func (f *eventForwarder) Event(e signins.RunEvent) {
	f.mu.Lock()
	h := f.hub
	f.mu.Unlock()
	if h == nil {
		slog.Info("converse: an errand's event before the hub, dropped", "run", e.Run, "kind", e.Kind)
		return
	}
	h.RunEvent(e)
}

// ownerSettings is converse.Settings over hostd: the agents answering (the primary, then the
// secondary) and the WhatsApp switch, and the owner's Contacts phone, read fresh each time.
type ownerSettings struct{ hostd *signins.HostdClient }

func (s ownerSettings) Answering(ctx context.Context) ([]string, bool, error) {
	primary, secondary, whatsapp, err := s.hostd.AgentSettings(ctx)
	if err != nil {
		return nil, false, err
	}
	var agents []string
	for _, name := range []string{primary, secondary} {
		if name != "" {
			agents = append(agents, name)
		}
	}
	return agents, whatsapp, nil
}

func (s ownerSettings) OwnerPhone(ctx context.Context) (string, error) {
	o, err := s.hostd.Owner(ctx)
	return o.ContactPhone, err
}

// converseConfig is the flags that start the owner's agent.
type converseConfig struct {
	DB, ClaudeSocket, CodexSocket, TimeZone, ConsoleURL string
	WhatsAppSocket, HostdSocket                         string
}

// startConverse opens the conversations' store, builds the hub over svc, hands it to events and runs
// it and the WhatsApp listener under ctx. The returned func waits for both to stop, closes the hub's
// intake and then the store; call it after ctx ends and the sign-ins runs have drained, so the
// errands the stop ended still reach their conversations. The handler is the console's agents and
// conversations API: with no
// -agent-db the agents' sign-in still answers and the conversations answer 503.
func startConverse(ctx context.Context, c converseConfig, svc *signins.Service, events *eventForwarder) (api http.Handler, wait func(), err error) {
	agents := map[string]agentv1connect.AgentServiceClient{}
	for name, socket := range map[string]string{"claude": c.ClaudeSocket, "codex": c.CodexSocket} {
		if socket != "" {
			agents[name] = agentv1connect.NewAgentServiceClient(&http.Client{Transport: h2c.Transport(socket)}, "http://agent")
		}
	}
	var states converse.AgentStates // hostd's connectors: an agent not added is not signed in, not down
	if c.HostdSocket != "" {
		states = signins.NewHostdClient(c.HostdSocket)
	}
	if c.DB == "" {
		return converse.NewHandler(nil, agents, states), func() {}, nil
	}
	if _, err := time.LoadLocation(c.TimeZone); err != nil {
		return nil, nil, fmt.Errorf("-time-zone %q: %w", c.TimeZone, err)
	}
	if svc == nil || c.HostdSocket == "" || c.WhatsAppSocket == "" {
		return nil, nil, errors.New("-agent-db needs -signins-db, -hostd-socket and -whatsapp-socket")
	}
	store, err := converse.OpenStore(c.DB)
	if err != nil {
		return nil, nil, err
	}
	settings := ownerSettings{hostd: signins.NewHostdClient(c.HostdSocket)}
	hub := converse.NewHub(converse.Config{
		Store:      store,
		Tools:      converse.NewTools(svc, store),
		Agents:     agents,
		Channels:   map[string]converse.Channel{"whatsapp": converse.NewWhatsAppChannel(c.WhatsAppSocket, settings)},
		Settings:   settings,
		TimeZone:   c.TimeZone,
		ConsoleURL: strings.TrimRight(c.ConsoleURL, "/"),
	})
	events.set(hub)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); hub.Run(ctx) }()
	go func() {
		defer wg.Done()
		converse.ListenWhatsApp(ctx, listen.NewClient(c.WhatsAppSocket), hub, settings)
	}()
	return converse.NewHandler(store, agents, states), sync.OnceFunc(func() {
		wg.Wait()
		hub.Close()
		store.Close()
	}), nil
}

// localZone is the box's IANA time zone name, the default of -time-zone.
func localZone() string { return zoneName(os.Getenv, os.Readlink) }

// zoneName is $TZ when it names a loadable zone, else the zoneinfo name /etc/localtime links to,
// else UTC (said in the log).
func zoneName(getenv func(string) string, readlink func(string) (string, error)) string {
	if tz := strings.TrimPrefix(getenv("TZ"), ":"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if link, err := readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(link, "zoneinfo/"); ok && name != "" {
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	slog.Warn("converse: the box's time zone is unknown, using UTC; set -time-zone")
	return "UTC"
}
