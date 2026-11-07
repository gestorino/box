//go:build live

package agent_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "concierge/engine/internal/gen/agent/v1"
	"concierge/engine/internal/gen/agent/v1/agentv1connect"
	"concierge/engine/internal/h2c"
)

// TestLiveStep sends one real Step to each daemon scripts/agent-live.sh started (its socket in
// LIVE_CLAUDE_SOCKET / LIVE_CODEX_SOCKET) and prints the answer and its usage. An agent whose home
// has no login is skipped, saying so. It prints no token: the contract carries none.
func TestLiveStep(t *testing.T) {
	for name, env := range map[string]string{"claudd": "LIVE_CLAUDE_SOCKET", "codexd": "LIVE_CODEX_SOCKET"} {
		t.Run(name, func(t *testing.T) {
			sock := os.Getenv(env)
			if sock == "" {
				t.Skipf("%s not set: this agent was not started", env)
			}
			c := agentv1connect.NewAgentServiceClient(&http.Client{Transport: h2c.Transport(sock)}, "http://x")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			st, err := c.Status(ctx, connect.NewRequest(&agentv1.StatusRequest{}))
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if !st.Msg.SignedIn {
				t.Skipf("%s: no login in its home, skipped", name)
			}
			t.Logf("%s: CLI %s, model %s", name, st.Msg.CliVersion, st.Msg.Model)
			res, err := c.Step(ctx, connect.NewRequest(&agentv1.StepRequest{
				Channel:  "whatsapp",
				TimeZone: "UTC",
				Now:      timestamppb.Now(),
				Events: []*agentv1.Event{{Id: 1, Kind: &agentv1.Event_Owner{
					Owner: "say hello in one short sentence"}}},
			}))
			if err != nil {
				t.Fatalf("step: %v", err)
			}
			say, ok := res.Msg.Action.(*agentv1.StepResponse_Say)
			if !ok {
				t.Fatalf("%s: expected a say, got %T", name, res.Msg.Action)
			}
			t.Logf("%s says: %q", name, say.Say)
			if u := res.Msg.Usage; u != nil {
				t.Logf("%s usage: model %s, %d in, %d out, cost %v (known: %v)", name, u.Model, u.InputTokens, u.OutputTokens, u.CostUsd, u.CostKnown)
			}
		})
	}
}
