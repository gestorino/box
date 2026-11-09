package claude

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	"concierge/engine/internal/agent"
)

// fakeCLI writes a `claude` shell script into a short temp dir and returns the dir, the script and
// the file it records argv, env and stdin in.
func fakeCLI(t *testing.T, body string) (dir, bin string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, bin
}

func homeWithToken(t *testing.T, token string) string {
	t.Helper()
	home, err := os.MkdirTemp("", "ch")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if token != "" {
		os.WriteFile(filepath.Join(home, "token"), []byte(token), 0o600)
	}
	return home
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func recordingScript(dir string) string {
	return `{ for a in "$@"; do printf 'ARG:%s\n' "$a"; done; env | sed 's/^/ENV:/'; printf 'STDIN:'; cat; printf '\nPWD:%s\n' "$PWD"; } > ` + dir + `/rec
cat ` + filepath.Join("testdata", "claude-reply.json") + "\n"
}

func TestRunPassesTheSchemaAndNoTools(t *testing.T) {
	dir, bin := fakeCLI(t, "")
	wd, _ := os.Getwd()
	os.WriteFile(bin, []byte("#!/bin/sh\n"+strings.Replace(recordingScript(dir), "testdata", wd+"/testdata", 1)), 0o755)
	home := homeWithToken(t, "sk-ant-oat01-secret\n")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-pass")
	t.Setenv("ANTHROPIC_BASE_URL", "http://elsewhere")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")

	schema := []byte(`{"type":"object"}`)
	if _, _, err := NewRunner(bin, home, "sonnet").Run(context.Background(), agent.Rules, "the owner's words", schema); err != nil {
		t.Fatal(err)
	}
	rec := read(t, filepath.Join(dir, "rec"))
	for _, want := range []string{
		"ARG:-p\n", "ARG:--output-format\nARG:json\n", "ARG:--json-schema\nARG:" + string(schema) + "\n",
		"ARG:--system-prompt\nARG:" + agent.Rules, "ARG:--model\nARG:sonnet\n",
		"ARG:--tools\nARG:\n", "ARG:--strict-mcp-config\n", "ARG:--setting-sources\nARG:\n",
		"ARG:--no-session-persistence\n", "ARG:--disable-slash-commands\n",
		"ENV:CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-secret\n", "ENV:DISABLE_AUTOUPDATER=1\n",
		"STDIN:the owner's words\n",
	} {
		if !strings.Contains(rec, want) {
			t.Errorf("record lacks %q:\n%s", want, rec)
		}
	}
	for _, bad := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK"} {
		if strings.Contains(rec, bad) {
			t.Errorf("%s reached the CLI", bad)
		}
	}
	if !strings.Contains(rec, "ENV:HOME="+home+"\n") {
		t.Error("HOME is not the daemon's home")
	}
	args := rec[:strings.Index(rec, "ENV:")]
	if strings.Contains(args, "the owner's words") || strings.Contains(args, "sk-ant") {
		t.Errorf("input or token in argv:\n%s", args)
	}
	if !strings.Contains(rec, "PWD:") || !strings.Contains(rec, filepath.Join(home, "work")) {
		t.Errorf("not run in home/work:\n%s", rec)
	}
}

func TestRunReadsStructuredOutputAndUsage(t *testing.T) {
	wd, _ := os.Getwd()
	_, bin := fakeCLI(t, "cat >/dev/null; cat "+wd+"/testdata/claude-reply.json\n")
	raw, u, err := NewRunner(bin, homeWithToken(t, "sk-ant-oat01-x"), "sonnet").Run(context.Background(), "s", "in", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"say"`)) || bytes.Contains(raw, []byte("total_cost_usd")) {
		t.Errorf("raw = %s", raw)
	}
	if u.Model != "claude-sonnet-4-5" || u.InputTokens != 460 || u.OutputTokens != 35 || u.CostUsd != 0.0123 || !u.CostKnown {
		t.Errorf("usage = %v", u)
	}
}

func TestRunFailures(t *testing.T) {
	for name, body := range map[string]string{
		"exit":      "echo 'boom: bad thing' >&2; echo second >&2; exit 3\n",
		"is_error":  "echo '{\"is_error\":true,\"result\":\"x\"}'; echo 'oops' >&2\n",
		"no output": "echo '{\"is_error\":false,\"result\":\"hi\"}'; echo 'oops' >&2\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, bin := fakeCLI(t, "cat >/dev/null\n"+body)
			_, _, err := NewRunner(bin, homeWithToken(t, "sk-ant-oat01-tok"), "m").Run(context.Background(), "s", "private words", []byte(`{}`))
			if err == nil {
				t.Fatal("no error")
			}
			if connect.CodeOf(err) != connect.CodeInternal {
				t.Errorf("code = %v", connect.CodeOf(err))
			}
			if strings.Contains(err.Error(), "private words") || strings.Contains(err.Error(), "sk-ant") {
				t.Errorf("error leaks: %v", err)
			}
		})
	}
	_, bin := fakeCLI(t, "cat >/dev/null\necho 'boom: bad thing' >&2; echo second >&2; exit 3\n")
	_, _, err := NewRunner(bin, homeWithToken(t, "sk-ant-oat01-tok"), "m").Run(context.Background(), "s", "i", []byte(`{}`))
	if !strings.Contains(err.Error(), "boom: bad thing") || strings.Contains(err.Error(), "second") {
		t.Errorf("want stderr's first line only: %v", err)
	}
}

func TestNotSignedInIsUnauthenticated(t *testing.T) {
	dir, bin := fakeCLI(t, "")
	os.WriteFile(bin, []byte("#!/bin/sh\ntouch "+dir+"/ran\n"), 0o755)
	_, _, err := NewRunner(bin, homeWithToken(t, ""), "m").Run(context.Background(), "s", "i", []byte(`{}`))
	if connect.CodeOf(err) != connect.CodeUnauthenticated || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("err = %v", err)
	}
	if _, e := os.Stat(filepath.Join(dir, "ran")); e == nil {
		t.Error("the CLI ran")
	}
}

func TestLimitIsResourceExhausted(t *testing.T) {
	wd, _ := os.Getwd()
	for name, tail := range map[string]string{"exit 0": "", "exit 1": "exit 1\n"} {
		t.Run(name, func(t *testing.T) {
			_, bin := fakeCLI(t, "cat >/dev/null; cat "+wd+"/testdata/claude-limit.json\n"+tail)
			_, _, err := NewRunner(bin, homeWithToken(t, "sk-ant-oat01-secret"), "m").Run(context.Background(), "s", "i", []byte(`{}`))
			if connect.CodeOf(err) != connect.CodeResourceExhausted {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "sk-ant") || strings.Contains(err.Error(), "1762300800") {
				t.Errorf("error leaks: %v", err)
			}
		})
	}
}

func TestASignInRefusedIsUnauthenticated(t *testing.T) {
	wd, _ := os.Getwd()
	for name, tail := range map[string]string{"exit 0": "", "exit 1": "exit 1\n"} {
		t.Run(name, func(t *testing.T) {
			_, bin := fakeCLI(t, "cat >/dev/null; cat "+wd+"/testdata/claude-auth.json\n"+tail)
			_, _, err := NewRunner(bin, homeWithToken(t, "sk-ant-oat01-secret"), "m").Run(context.Background(), "s", "i", []byte(`{}`))
			if connect.CodeOf(err) != connect.CodeUnauthenticated {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "sk-ant") || strings.Contains(err.Error(), "API key") || strings.Contains(err.Error(), "/login") {
				t.Errorf("error leaks: %v", err)
			}
		})
	}
}

// setupTokenScript draws as the real CLI does: words placed by column (ESC[nG), rows reached by
// cursor moves (ESC[nB), never a newline; it ends half a second after showing the token.
const setupTokenScript = `if [ "$1" = "--version" ]; then echo "2.1.319 (Claude Code)"; exit 0; fi
[ "$1" = "setup-token" ] || exit 9
printf '\033[2J\033[H Browser\033[10Gdidn'"'"'t\033[17Gopen?\r\033[2B\033[1Chttps://claude.ai/oauth/authorize?code=true&client_id=x\r\033[2B\033[1CPaste\033[8Gcode\033[13Ghere\033[18G>\033[20G'
read code
[ "$code" = "abc" ] || { printf '\r\033[2B\033[1COAuth\033[7Gerror\r\033[1B'; exit 1; }
printf '\r\033[2B\033[1C\342\234\223\033[4GLong-lived\033[15Gtoken\033[21Gcreated\r\033[2B\033[1CYour\033[6GOAuth\033[12Gtoken:\r\033[2B\033[1C\033[33msk-ant-oat01-x\033[39m\r\033[2B\033[1CStore\033[7Gthis\033[12Gtoken\033[18Gsecurely.\r\033[1B'
sleep 0.5
`

func TestLoginStartGivesTheLinkAndFinishKeepsTheToken(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	_, bin := fakeCLI(t, setupTokenScript)
	home := filepath.Join(homeWithToken(t, ""), "agent")
	l := NewLogin(bin, home)
	ctx := context.Background()
	start, err := l.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if start.Url != "https://claude.ai/oauth/authorize?code=true&client_id=x" || !start.NeedsCode {
		t.Fatalf("start = %v", start)
	}
	if err := l.Finish(ctx, "bad"); err == nil || strings.Contains(err.Error(), "sk-ant") {
		t.Fatalf("bad code: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "token")); err == nil {
		t.Fatal("a token after a bad code")
	}
	if _, err := l.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l.Finish(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "token")
	if got := read(t, p); got != "sk-ant-oat01-x" {
		t.Errorf("token file = %q", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(home); fi.Mode().Perm() != 0o700 {
		t.Errorf("home mode = %v", fi.Mode().Perm())
	}
	if strings.Contains(logs.String(), "sk-ant") {
		t.Error("the token reached the log")
	}
	if err := l.Finish(ctx, "abc"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("finish with nothing pending: %v", err)
	}
}

func TestLogoutDeletesTheToken(t *testing.T) {
	home := homeWithToken(t, "sk-ant-oat01-x")
	_, bin := fakeCLI(t, "")
	l := NewLogin(bin, home)
	if err := l.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "token")); err == nil {
		t.Error("token still there")
	}
	if err := l.Logout(context.Background()); err != nil {
		t.Errorf("logout when signed out: %v", err)
	}
}

func TestStatusReadsVersion(t *testing.T) {
	_, bin := fakeCLI(t, setupTokenScript)
	home := homeWithToken(t, "")
	l := NewLogin(bin, home)
	s, err := l.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.CliVersion != "2.1.319 (Claude Code)" || s.SignedIn {
		t.Errorf("status = %v", s)
	}
	os.WriteFile(filepath.Join(home, "token"), []byte("t"), 0o600)
	if s, _ = l.Status(context.Background()); !s.SignedIn {
		t.Error("not signed in with a token")
	}
}

func TestTheTokenIsKeptWholeWhenItsRowArrivesInTwoReads(t *testing.T) {
	_, bin := fakeCLI(t, `[ "$1" = "setup-token" ] || exit 9
printf '\033[1Chttps://claude.ai/oauth/authorize?x=1\r\033[2B'
read code
printf '\r\033[2B\033[1Csk-ant-oat01-ab'
sleep 0.05
printf 'cdef\r\033[2B\033[1CStore'
sleep 0.5
`)
	home := filepath.Join(homeWithToken(t, ""), "agent")
	l := NewLogin(bin, home)
	if _, err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := l.Finish(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(home, "token")); got != "sk-ant-oat01-abcdef" {
		t.Errorf("token = %q", got)
	}
}

// pidsOf reads the pids a fake wrote, one per line.
func pidsOf(t *testing.T, p string) []int {
	t.Helper()
	var pids []int
	for _, f := range strings.Fields(read(t, p)) {
		n, err := strconv.Atoi(f)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, n)
	}
	return pids
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("process %d is still running", pid)
}

func waitFile(t *testing.T, p string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never written", p)
}

func shorten(t *testing.T, v *time.Duration, d time.Duration) {
	old := *v
	*v = d
	t.Cleanup(func() { *v = old })
}

func TestASecondStartKillsThePendingSetupToken(t *testing.T) {
	dir, bin := fakeCLI(t, "")
	pids := filepath.Join(dir, "pids")
	os.WriteFile(bin, []byte("#!/bin/sh\necho $$ >> "+pids+"\necho 'https://x.example/y'\nsleep 30\n"), 0o755)
	l := NewLogin(bin, filepath.Join(homeWithToken(t, ""), "agent"))
	t.Cleanup(func() { l.Logout(context.Background()) })
	if _, err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := pidsOf(t, pids)
	if len(got) != 2 {
		t.Fatalf("pids = %v", got)
	}
	waitGone(t, got[0])
	if syscall.Kill(got[1], 0) != nil {
		t.Error("the new sign-in is not running")
	}
	l.Logout(context.Background())
	waitGone(t, got[1])
}

func TestACancelledStartAndAWaitTimeoutKillTheSetupToken(t *testing.T) {
	dir, bin := fakeCLI(t, "")
	pids := filepath.Join(dir, "pids")
	os.WriteFile(bin, []byte("#!/bin/sh\necho $$ >> "+pids+"\nsleep 30\n"), 0o755)
	l := NewLogin(bin, filepath.Join(homeWithToken(t, ""), "agent"))

	// Cancelled once the CLI is running (its pid written), not on a timer: a loaded machine may not
	// have started it yet, and a CLI killed before it runs leaves nothing to check.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if b, err := os.ReadFile(pids); err == nil && len(b) > 0 {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if _, err := l.Start(ctx); err == nil {
		t.Fatal("Start without a link succeeded")
	}
	waitFile(t, pids)
	waitGone(t, pidsOf(t, pids)[0])

	// A wait that times out: the link comes, the token never.
	os.WriteFile(bin, []byte("#!/bin/sh\necho $$ >> "+pids+"\necho 'https://x.example/y'\nread code\nsleep 30\n"), 0o755)
	if _, err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	shorten(t, &loginWait, 300*time.Millisecond)
	if err := l.Finish(context.Background(), "abc"); err == nil {
		t.Fatal("Finish without a token succeeded")
	}
	got := pidsOf(t, pids)
	if len(got) != 2 {
		t.Fatalf("pids = %v", got)
	}
	waitGone(t, got[1])
}

func TestRunKillsTheCLIOnTimeoutEvenWithAGrandchildHoldingItsPipes(t *testing.T) {
	dir, bin := fakeCLI(t, "")
	pids := filepath.Join(dir, "pids")
	os.WriteFile(bin, []byte("#!/bin/sh\necho $$ > "+pids+"\nsleep 30 &\necho $! >> "+pids+"\nsleep 30\n"), 0o755)
	shorten(t, &runTimeout, 1000*time.Millisecond)
	shorten(t, &waitDelay, 300*time.Millisecond)
	start := time.Now()
	_, _, err := NewRunner(bin, homeWithToken(t, "sk-ant-oat01-x"), "m").Run(context.Background(), "s", "i", []byte(`{}`))
	if err == nil || connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("Run took %v", time.Since(start))
	}
	got := pidsOf(t, pids)
	if len(got) != 2 {
		t.Fatalf("pids = %v", got)
	}
	waitGone(t, got[0])
	waitGone(t, got[1]) // the grandchild dies with the CLI's group
}

// A Start or a Logout while a Finish waits ends that sign-in, and the Finish with it, promptly.
func TestAStartOrLogoutDuringFinishEndsTheWait(t *testing.T) {
	for name, interrupt := range map[string]func(*Login){
		"start":  func(l *Login) { l.Start(context.Background()) },
		"logout": func(l *Login) { l.Logout(context.Background()) },
	} {
		t.Run(name, func(t *testing.T) {
			dir, bin := fakeCLI(t, "")
			pids := filepath.Join(dir, "pids")
			os.WriteFile(bin, []byte("#!/bin/sh\necho $$ >> "+pids+"\necho 'https://x.example/y'\nread code\nsleep 30\n"), 0o755)
			l := NewLogin(bin, filepath.Join(homeWithToken(t, ""), "agent"))
			t.Cleanup(func() { l.Logout(context.Background()) })
			if _, err := l.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			res := make(chan error, 1)
			go func() { res <- l.Finish(context.Background(), "abc") }()
			time.Sleep(300 * time.Millisecond)
			begun := time.Now()
			interrupt(l)
			select {
			case err := <-res:
				if err == nil {
					t.Error("Finish succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Finish still waits")
			}
			if time.Since(begun) > 2*time.Second {
				t.Errorf("took %v", time.Since(begun))
			}
			waitGone(t, pidsOf(t, pids)[0])
		})
	}
}
