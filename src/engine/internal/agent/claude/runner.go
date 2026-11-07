// Package claude runs the owner's agent on Claude Code, signed in on the owner's plan: the CLI's
// print mode for a step, its setup-token for the sign-in. The login (a long-lived token) lives in
// the daemon's home and nowhere else.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentv1 "concierge/engine/internal/gen/agent/v1"
)

const runTimeout = 120 * time.Second

// Runner runs one model step through the claude CLI.
type Runner struct{ bin, home, model string }

// NewRunner returns a Runner of the claude binary bin, whose home holds the token and the empty
// working directory.
func NewRunner(bin, home, model string) *Runner { return &Runner{bin, home, model} }

var errNotSignedIn = connect.NewError(connect.CodeFailedPrecondition, errors.New("not signed in"))

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
	cmd.Env = append(cliEnv(), "CLAUDE_CODE_OAUTH_TOKEN="+token, "DISABLE_AUTOUPDATER=1", "HOME="+r.home)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, fail("claude failed", stderr.String(), token, err)
	}
	var rep cliReply
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		return nil, nil, fail("claude's reply is not JSON", stderr.String(), token, nil)
	}
	if rep.IsError {
		return nil, nil, fail("claude reported an error", stderr.String(), token, nil)
	}
	if len(rep.StructuredOutput) == 0 || string(rep.StructuredOutput) == "null" {
		return nil, nil, fail("claude gave no structured output", stderr.String(), token, nil)
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

// fail is an Internal error naming what went wrong and stderr's first line, never the input or the token.
func fail(what, stderr, token string, err error) error {
	line, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n")
	line = strings.ReplaceAll(strings.TrimSpace(line), token, "[token]")
	msg := what
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg += fmt.Sprintf(" (%s)", ee.ProcessState)
	} else if err != nil {
		msg += " (" + err.Error() + ")"
	}
	if line != "" {
		msg += ": " + line
	}
	return connect.NewError(connect.CodeInternal, errors.New(msg))
}

// cliEnv is this process's environment without any other credential the CLI would prefer to the
// owner's token.
func cliEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "ANTHROPIC_API_KEY" || k == "ANTHROPIC_AUTH_TOKEN" || k == "CLAUDE_CODE_OAUTH_TOKEN" || k == "HOME" {
			continue
		}
		env = append(env, kv)
	}
	return env
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
