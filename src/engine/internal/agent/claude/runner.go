// Package claude runs the owner's agent on Claude Code, signed in on the owner's plan: the CLI's
// print mode for a step, its setup-token for the sign-in. The login (a long-lived token) lives in
// the daemon's home and nowhere else.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	"concierge/engine/internal/agent/cliproc"
	agentv1 "concierge/engine/internal/gen/agent/v1"
)

// Variables so tests can shorten them.
var (
	runTimeout = 120 * time.Second
	waitDelay  = 3 * time.Second
)

// Runner runs one model step through the claude CLI.
type Runner struct{ bin, home, model string }

// NewRunner returns a Runner of the claude binary bin, whose home holds the token and the empty
// working directory.
func NewRunner(bin, home, model string) *Runner { return &Runner{bin, home, model} }

var errNotSignedIn = connect.NewError(connect.CodeUnauthenticated, errors.New("not signed in"))

// cliReply is the part of `--output-format json`'s result that matters.
type cliReply struct {
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     *float64        `json:"total_cost_usd"`
	Usage            struct {
		InputTokens         int64 `json:"input_tokens"`
		CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadTokens     int64 `json:"cache_read_input_tokens"`
		OutputTokens        int64 `json:"output_tokens"`
	} `json:"usage"`
	ModelUsage map[string]json.RawMessage `json:"modelUsage"`
}

// Run implements agent.Runner. The input goes on stdin, never in argv.
func (r *Runner) Run(ctx context.Context, system, input string, schema []byte) ([]byte, *agentv1.Usage, error) {
	token, err := readToken(r.home)
	if err != nil {
		return nil, nil, err
	}
	work := filepath.Join(r.home, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		return nil, nil, connect.NewError(connect.CodeInternal, err)
	}
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin,
		"-p",
		"--output-format", "json",
		"--json-schema", string(schema),
		"--system-prompt", system,
		"--model", r.model,
		"--tools", "", // no built-in tool
		"--strict-mcp-config",   // with no --mcp-config: no MCP server
		"--setting-sources", "", // no user, project or local settings
		"--no-session-persistence",
		"--disable-slash-commands",
	)
	cmd.Dir = work
	cliproc.KillOnCancel(cmd, waitDelay) // a timeout kills the CLI's whole group
	cmd.Env = cliEnv("CLAUDE_CODE_OAUTH_TOKEN="+token, "HOME="+r.home)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var rep cliReply
	jsonErr := json.Unmarshal(stdout.Bytes(), &rep)
	if jsonErr == nil && rep.IsError && cliproc.LimitReached(rep.Result) { // the CLI may exit non-zero with it
		return nil, nil, cliproc.ErrLimit
	}
	if runErr != nil {
		return nil, nil, cliproc.Fail("claude failed", stderr.String(), runErr, token)
	}
	if jsonErr != nil {
		return nil, nil, cliproc.Fail("claude's reply is not JSON", stderr.String(), nil, token)
	}
	if rep.IsError {
		return nil, nil, cliproc.Fail("claude reported an error", stderr.String(), nil, token)
	}
	if len(rep.StructuredOutput) == 0 || string(rep.StructuredOutput) == "null" {
		return nil, nil, cliproc.Fail("claude gave no structured output", stderr.String(), nil, token)
	}
	u := &agentv1.Usage{
		Model:        r.model,
		InputTokens:  rep.Usage.InputTokens + rep.Usage.CacheCreationTokens + rep.Usage.CacheReadTokens,
		OutputTokens: rep.Usage.OutputTokens,
	}
	for m := range rep.ModelUsage {
		u.Model = m
		break
	}
	if rep.TotalCostUSD != nil {
		u.CostUsd, u.CostKnown = *rep.TotalCostUSD, true
	}
	return rep.StructuredOutput, u, nil
}

// cliEnv is this process's environment without any ANTHROPIC_ or CLAUDE_CODE_ setting (another
// credential, endpoint or provider the CLI would prefer to the owner's token), then set.
func cliEnv(set ...string) []string {
	return cliproc.Env([]string{"ANTHROPIC_", "CLAUDE_CODE_"}, append(set, "DISABLE_AUTOUPDATER=1")...)
}

func readToken(home string) (string, error) {
	b, err := os.ReadFile(filepath.Join(home, "token"))
	if errors.Is(err, os.ErrNotExist) {
		return "", errNotSignedIn
	}
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, errors.New("reading the token failed"))
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", errNotSignedIn
	}
	return t, nil
}
