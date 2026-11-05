package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

// daemonCheck is where a connector's setup check is read: its daemon's proxy and the check's path.
type daemonCheck struct {
	h    http.Handler
	path string
}

// confirm is POST /api/v1/connectors/{name}/confirm: the engine asks the connector's daemon whether
// its check was answered and only then tells hostd the connector is verified. The console has no
// other way to hostd's /connectors/{name}/verified, and the daemons have no write path to hostd at
// all. Any refusal along the way is passed on as the daemon or hostd gave it.
func confirm(hostd http.Handler, checks map[string]daemonCheck) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		c, ok := checks[name]
		if !ok {
			writeStatus(w, http.StatusNotFound, map[string]string{"error": "no such connector"})
			return
		}
		rec := ask(r.Context(), c.h, http.MethodGet, c.path)
		if rec.Code != http.StatusOK {
			pass(w, rec)
			return
		}
		var check struct {
			State string `json:"state"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &check) != nil || check.State != "passed" {
			writeStatus(w, http.StatusConflict, map[string]string{"error": "not verified yet"})
			return
		}
		pass(w, ask(r.Context(), hostd, http.MethodPost, "/api/v1/connectors/"+name+"/verified"))
	})
}

// knownConnector passes on only a request whose {name} is one of the connectors.
func knownConnector(checks map[string]daemonCheck, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := checks[r.PathValue("name")]; !ok {
			writeStatus(w, http.StatusNotFound, map[string]string{"error": "no such connector"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ask calls a service's proxy in-process: the proxy already holds the service's socket, so the
// engine reuses it rather than dialling the socket a second way.
func ask(ctx context.Context, h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r, err := http.NewRequestWithContext(ctx, method, "http://engine"+path, nil)
	if err != nil {
		writeStatus(rec, http.StatusInternalServerError, map[string]string{"error": "bad request to a service"})
		return rec
	}
	h.ServeHTTP(rec, r)
	return rec
}

// pass writes a service's answer as it came.
func pass(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(rec.Code)
	w.Write(rec.Body.Bytes())
}

func writeStatus(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// orUnconfigured stands in for a daemon the engine was started without.
func orUnconfigured(h http.Handler, name string) http.Handler {
	if h != nil {
		return h
	}
	return SocketProxy("", name)
}
