package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"concierge/engine/internal/channels"
)

// agentsStub stands in for converse's handler: its check answers by signedIn, every other route
// echoes the method and path.
func agentsStub(signedIn *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agents/claude/check" {
			state := "pending"
			if signedIn.Load() {
				state = "passed"
			}
			w.Write([]byte(`{"state":"` + state + `"}`))
			return
		}
		w.Write([]byte(r.Method + " " + r.URL.Path))
	})
}

func TestConfirmAnAgentNeedsItSignedIn(t *testing.T) {
	hostd, hs := serveAnswering(t)
	hostd.set("POST /connectors/claude/verified", http.StatusNoContent, "")
	var signedIn atomic.Bool
	reg, _ := channels.NewRegistry()
	h := New(Config{Login: testLogin, Channels: reg, Network: SocketProxy(hs, "host service"), Agents: agentsStub(&signedIn)})
	if rec := send(h, "POST", "/api/v1/connectors/claude/confirm", "", sameOriginJSON); rec.Code != http.StatusConflict {
		t.Fatalf("signed out: %d %s", rec.Code, rec.Body)
	}
	if len(hostd.got()) != 0 {
		t.Fatalf("hostd saw %q", hostd.got())
	}
	signedIn.Store(true)
	if rec := send(h, "POST", "/api/v1/connectors/claude/confirm", "", sameOriginJSON); rec.Code != http.StatusNoContent {
		t.Fatalf("signed in: %d %s", rec.Code, rec.Body)
	}
	if got := strings.Join(hostd.got(), "|"); got != "POST /connectors/claude/verified" {
		t.Fatalf("hostd saw %q", got)
	}
	// Add of an agent reaches hostd like any connector's.
	hostd.set("POST /connectors/codex", http.StatusNoContent, "")
	if rec := send(h, "POST", "/api/v1/connectors/codex", "", sameOriginJSON); rec.Code != http.StatusNoContent {
		t.Fatalf("add codex: %d %s", rec.Code, rec.Body)
	}
}

func TestAgentRoutesAreBehindTheLoginAndTheCheckIsNotRouted(t *testing.T) {
	reg, _ := channels.NewRegistry()
	var signedIn atomic.Bool
	h := New(Config{Login: testLogin, Channels: reg, Agents: agentsStub(&signedIn)})
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/agents"}, {"POST", "/api/v1/agents/claude/login"}, {"POST", "/api/v1/agents/claude/login/finish"},
		{"POST", "/api/v1/agents/codex/logout"}, {"GET", "/api/v1/conversations"}, {"GET", "/api/v1/conversations/3"},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without the login: %d", c.method, c.path, rec.Code)
		}
		r.SetBasicAuth("helper", "helper")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK || rec.Body.String() != c.method+" "+c.path {
			t.Errorf("%s %s with the login: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if rec := get(t, h, "/api/v1/agents/claude/check"); rec.Code != http.StatusNotFound {
		t.Errorf("the check is the engine's own: %d %s", rec.Code, rec.Body)
	}
}
