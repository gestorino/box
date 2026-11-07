// Package api is the engine's HTTP surface: the JSON API under /api/v1 and, optionally, the
// built UI (src/ui/dist) at /.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"concierge/engine/internal/browser"
	"concierge/engine/internal/channels"
)

// Browser is what the API needs from the browser worker.
type Browser interface {
	Status(ctx context.Context) browser.Status
}

type BrowserStatus struct {
	Enabled bool `json:"enabled"`
	browser.Status
}

// Build identifies the running engine: the release version and the commit it was built from,
// both stamped in at build time (`-ldflags -X`, see ansible/deploy.yml).
type Build struct {
	Version  string
	Revision string
}

type StatusResponse struct {
	Version  string        `json:"version"`
	Revision string        `json:"revision,omitempty"`
	Browser  BrowserStatus `json:"browser"`
	Channels []string      `json:"channels"`
}

// Config is what the HTTP surface serves. Browser may be nil (the worker is not configured);
// UIDir may be empty; Network is the proxy to hostd (NetworkProxy).
type Config struct {
	Login    Login
	Build    Build
	Browser  Browser
	Channels *channels.Registry
	UIDir    string
	Network  http.Handler // hostd: /api/v1/network/*, /api/v1/settings/*, /api/v1/connectors
	SMS      http.Handler // smsd: /api/v1/sms/* (reads, read marks and remove)
	Calls    http.Handler // calld: /api/v1/calls (reads, seen mark and remove)
	WhatsApp http.Handler // whatsappd: /api/v1/whatsapp/*
	Email    http.Handler // emaild: /api/v1/email/*
	Telegram http.Handler // telegramd: /api/v1/telegram/*
	SignIns  http.Handler // the run machinery's own routes: /api/v1/signins/sites/{site}/… (screen, live, recording)
	Accounts http.Handler // the catalog and the box's accounts: /api/v1/accounts, GET POST PUT DELETE
	Runs     http.Handler // the engine's agent runs: /api/v1/runs, POST /api/v1/errands to start one, /api/v1/appointments
	Health   http.Handler // the box's health history: /api/v1/health/{now,panels,events}; nil answers 503
	AI       http.Handler // the OpenRouter key (aikey): /api/v1/ai, /api/v1/ai/key and /api/v1/ai/check
	Agents   http.Handler // the owner's agent (converse): /api/v1/agents, their sign-in, /api/v1/conversations; its GET /api/v1/agents/{name}/check is the agents' connector check, asked in-process
}

// New builds the handler: everything but /healthz requires the console login, and every write under
// /api/ passes guardWrites. An empty Login lets nothing through.
func New(c Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		resp := StatusResponse{Version: c.Build.Version, Revision: c.Build.Revision, Channels: c.Channels.Names()}
		if c.Browser != nil {
			resp.Browser = BrowserStatus{Enabled: true, Status: c.Browser.Status(r.Context())}
		}
		writeJSON(w, resp)
	})
	if c.Network != nil {
		// One pattern per method: a method-less "/api/v1/network/" would conflict with "GET /api/".
		for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
			mux.Handle(m+" /api/v1/network/", c.Network)
		}
		mux.Handle("GET /api/v1/settings/", c.Network)
		mux.Handle("PUT /api/v1/settings/", c.Network)
		mux.Handle("DELETE /api/v1/settings/account/card", c.Network) // the one delete under settings
		checks := map[string]daemonCheck{
			"email":    {orUnconfigured(c.Email, "email service"), "/api/v1/email/check"},
			"voice":    {orUnconfigured(c.Calls, "call service"), "/api/v1/calls/check"},
			"sms":      {orUnconfigured(c.SMS, "SMS service"), "/api/v1/sms/check"},
			"whatsapp": {orUnconfigured(c.WhatsApp, "WhatsApp service"), "/api/v1/whatsapp/check"},
			"telegram": {orUnconfigured(c.Telegram, "Telegram service"), "/api/v1/telegram/check"},
		}
		if c.Agents != nil {
			// The agents' check is the engine's own: signed in is passed. The route is not in the mux.
			checks["claude"] = daemonCheck{c.Agents, "/api/v1/agents/claude/check"}
			checks["codex"] = daemonCheck{c.Agents, "/api/v1/agents/codex/check"}
		}
		// Exactly add and remove of the five: hostd's /connectors/{name}/verified is the engine's to
		// call (confirm), so nothing else — an encoded slash in {name} included — reaches hostd.
		mux.Handle("GET /api/v1/connectors", c.Network)
		mux.Handle("POST /api/v1/connectors/{name}", knownConnector(checks, c.Network))
		mux.Handle("DELETE /api/v1/connectors/{name}", knownConnector(checks, c.Network))
		mux.Handle("POST /api/v1/connectors/{name}/confirm", confirm(c.Network, checks))
	}
	if c.SMS != nil {
		mux.Handle("GET /api/v1/sms/", c.SMS)
		mux.Handle("POST /api/v1/sms/remove", c.SMS)
		mux.Handle("POST /api/v1/sms/read", c.SMS)
		mux.Handle("POST /api/v1/sms/send", c.SMS)
		mux.Handle("POST /api/v1/sms/check", c.SMS)
	}
	if c.Calls != nil {
		mux.Handle("GET /api/v1/calls", c.Calls)
		mux.Handle("GET /api/v1/calls/", c.Calls) // /{id}/turns and /{id}/audio; calld 404s the rest
		mux.Handle("POST /api/v1/calls/remove", c.Calls)
		mux.Handle("POST /api/v1/calls/seen", c.Calls)
		mux.Handle("POST /api/v1/calls/call", c.Calls)
		mux.Handle("GET /api/v1/calls/check", c.Calls)
		mux.Handle("POST /api/v1/calls/check", c.Calls)
		mux.Handle("GET /api/v1/calls/key", c.Calls) // the owner's OpenAI key, kept by calld alone
		mux.Handle("PUT /api/v1/calls/key", c.Calls)
	}
	if c.WhatsApp != nil {
		mux.Handle("GET /api/v1/whatsapp/", c.WhatsApp)
		mux.Handle("POST /api/v1/whatsapp/", c.WhatsApp) // link, unlink
	}
	if c.Email != nil {
		mux.Handle("GET /api/v1/email/", c.Email)
		mux.Handle("POST /api/v1/email/", c.Email) // connect, disconnect
	}
	if c.Telegram != nil {
		mux.Handle("GET /api/v1/telegram/", c.Telegram)
		mux.Handle("POST /api/v1/telegram/", c.Telegram) // credentials, link, password, settings, unlink
	}
	if c.Runs != nil {
		mux.Handle("POST /api/v1/errands", c.Runs)
		mux.Handle("GET /api/v1/runs", c.Runs)
		for _, m := range []string{"GET", "POST"} {
			mux.Handle(m+" /api/v1/runs/", c.Runs)
		}
		mux.Handle("GET /api/v1/appointments", c.Runs)
		mux.Handle("POST /api/v1/appointments/", c.Runs)
		mux.Handle("DELETE /api/v1/appointments/", c.Runs)
	}
	if c.Agents != nil {
		mux.Handle("GET /api/v1/agents", c.Agents)
		mux.Handle("POST /api/v1/agents/{name}/login", c.Agents)
		mux.Handle("POST /api/v1/agents/{name}/login/finish", c.Agents)
		mux.Handle("POST /api/v1/agents/{name}/logout", c.Agents)
		mux.Handle("GET /api/v1/conversations", c.Agents)
		mux.Handle("GET /api/v1/conversations/{id}", c.Agents)
	}
	if c.AI != nil {
		mux.Handle("GET /api/v1/ai", c.AI)
		mux.Handle("PUT /api/v1/ai/key", c.AI)
		mux.Handle("DELETE /api/v1/ai/key", c.AI)
		mux.Handle("POST /api/v1/ai/check", c.AI)
	}
	if c.SignIns != nil {
		for _, m := range []string{"GET", "POST"} {
			mux.Handle(m+" /api/v1/signins/", c.SignIns)
		}
	}
	if c.Accounts != nil {
		for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
			mux.Handle(m+" /api/v1/accounts", c.Accounts)
			mux.Handle(m+" /api/v1/accounts/", c.Accounts)
		}
	}
	health := c.Health
	if health == nil {
		health = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			unavailable(w, "health data unavailable")
		})
	}
	for _, p := range []string{"now", "panels", "events"} {
		mux.Handle("GET /api/v1/health/"+p, health)
	}
	mux.Handle("GET /api/", http.NotFoundHandler())
	if c.UIDir != "" {
		mux.Handle("GET /", ui(c.UIDir))
	}
	return requireLogin(c.Login, guardWrites(mux))
}

// ui serves the built single-page app: a path naming one of the build's files (under /assets/, or a
// top-level name with an extension, /favicon.svg) is that file or a 404; any other path is a route
// the app resolves in the browser, so it gets index.html — a route may hold a dot
// (/accounts/example.com).
// Vite names everything under assets/ by its content hash, so those files never change; every other
// file, the page above all, is revalidated on each load so a deploy shows at once.
func ui(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		file := strings.HasPrefix(r.URL.Path, "/assets/") || (path.Dir(r.URL.Path) == "/" && path.Ext(r.URL.Path) != "")
		if r.URL.Path == "/" || file {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, index)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("api: response not written", "err", err)
	}
}
