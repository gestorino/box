// Package codex runs the owner's agent on Codex, signed in on the owner's plan: the CLI's exec mode
// for a step, its device login for the sign-in. The login (auth.json) lives in the daemon's home,
// under CODEX_HOME, and nowhere else.
package codex

import (
	"bufio"
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

// Runner runs one model step through the codex CLI.
type Runner struct{ bin, home, model string }

// NewRunner returns a Runner of the codex binary bin, whose home holds CODEX_HOME (.codex) and the
// empty working directory (work).
func NewRunner(bin, home, model string) *Runner { return &Runner{bin, home, model} }

var errNotSignedIn = connect.NewError(connect.CodeFailedPrecondition, errors.New("not signed in"))

// event is the part of `codex exec --json`'s lines that matters.
type event struct {
	Type string `json:"type"`
	Item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"` // counts the cached ones too
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

// Run implements agent.Runner. The rules and the input go on stdin, never in argv. The CLI may
// not run a shell command (shell tool off) and its sandbox is read-only besides.
func (r *Runner) Run(ctx context.Context, system, input string, schema []byte) ([]byte, *agentv1.Usage, error) {
	if _, err := os.Stat(filepath.Join(codexHome(r.home), "auth.json")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, errNotSignedIn
		}
		return nil, nil, connect.NewError(connect.CodeInternal, errors.New("reading the sign-in failed"))
	}
	work := filepath.Join(r.home, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		return nil, nil, connect.NewError(connect.CodeInternal, err)
	}
	sf, err := os.CreateTemp(work, "schema-*.json")
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeInternal, err)
	}
	defer os.Remove(sf.Name())
	if _, err := sf.Write(schema); err != nil {
		sf.Close()
		return nil, nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := sf.Close(); err != nil {
		return nil, nil, connect.NewError(connect.CodeInternal, err)
	}

	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin,
		"exec",
		"--json",
		"--output-schema", sf.Name(),
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"-C", work,
		"-m", r.model,
		"--disable", "shell_tool", // the agent runs no shell command
		"--ephemeral",          // no session files
		"--ignore-user-config", // no config.toml (auth still uses CODEX_HOME)
		"--ignore-rules",       // no execpolicy rules
	)
	cmd.Dir = work
	cmd.Env = cliEnv(r.home)
	cmd.Stdin = strings.NewReader(system + "\n\n" + input)
	cliproc.KillOnCancel(cmd, waitDelay)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, cliproc.Fail("codex failed", stderr.String(), err)
	}

	var raw string
	var u agentv1.Usage
	u.Model = r.model
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var ev event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "turn.failed" || ev.Type == "error":
			return nil, nil, cliproc.Fail("codex reported an error", stderr.String(), nil)
		case ev.Type == "item.completed" && ev.Item.Type == "agent_message":
			raw = ev.Item.Text
		case ev.Type == "turn.completed":
			u.InputTokens += ev.Usage.InputTokens
			u.OutputTokens += ev.Usage.OutputTokens
		}
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil, cliproc.Fail("codex gave no final message", stderr.String(), nil)
	}
	return []byte(raw), &u, nil
}

func codexHome(home string) string { return filepath.Join(home, ".codex") }

// cliEnv is this process's environment without any OPENAI_ or CODEX_ setting (another credential,
// endpoint or home the CLI would prefer), with CODEX_HOME and HOME set to the daemon's.
func cliEnv(home string) []string {
	return cliproc.Env([]string{"OPENAI_", "CODEX_"}, "CODEX_HOME="+codexHome(home), "HOME="+home)
}
