package codex

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
)

// fakeCLI writes a `codex` shell script into a short temp dir.
func fakeCLI(t *testing.T, body string) (dir, bin string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	bin = filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, bin
}

func newHome(t *testing.T, signedIn bool) string {
	t.Helper()
	home, err := os.MkdirTemp("", "ch")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if signedIn {
		os.MkdirAll(filepath.Join(home, ".codex"), 0o700)
		os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"tokens":{"access_token":"secret-tok"}}`), 0o600)
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
	wd, _ := os.Getwd()
	return `{ for a in "$@"; do printf 'ARG:%s\n' "$a"; done; env | sed 's/^/ENV:/'; printf 'STDIN:'; cat; printf '\nPWD:%s\n' "$PWD";
  prev=; for a in "$@"; do [ "$prev" = --output-schema ] && { printf 'SCHEMA:'; cat "$a"; printf '\n'; }; prev=$a; done; } > ` + dir + `/rec
cat ` + wd + `/testdata/codex-events.jsonl
`
}

func TestExecIsReadOnlyWithTheSchema(t *testing.T) {
	dir, bin := fakeCLI(t, "")
	os.WriteFile(bin, []byte("#!/bin/sh\n"+recordingScript(dir)), 0o755)
	home := newHome(t, true)
	t.Setenv("OPENAI_API_KEY", "must-not-pass")
	t.Setenv("CODEX_API_KEY", "must-not-pass")
	t.Setenv("OPENAI_BASE_URL", "http://elsewhere")
	t.Setenv("CODEX_SANDBOX", "x")

	schema := []byte(`{"type":"object"}`)
	if _, _, err := NewRunner(bin, home, "gpt-x").Run(context.Background(), "the rules", "the owner's words", schema); err != nil {
		t.Fatal(err)
	}
	rec := read(t, filepath.Join(dir, "rec"))
	work := filepath.Join(home, "work")
	for _, want := range []string{
		"ARG:exec\n", "ARG:--json\n", "ARG:--output-schema\nARG:" + work + "/",
		"ARG:--sandbox\nARG:read-only\n", "ARG:--skip-git-repo-check\n", "ARG:-C\nARG:" + work + "\n",
		"ARG:-m\nARG:gpt-x\n", "ARG:--disable\nARG:shell_tool\n", "ARG:--ephemeral\n", "ARG:--ignore-user-config\n", "ARG:--ignore-rules\n",
		"ENV:CODEX_HOME=" + home + "/.codex\n", "ENV:HOME=" + home + "\n",
		"SCHEMA:" + string(schema) + "\n", "the owner's words",
	} {
		if !strings.Contains(rec, want) {
			t.Errorf("record lacks %q:\n%s", want, rec)
		}
	}
	if !strings.Contains(rec, "STDIN:the rules") {
		t.Errorf("rules are not first on stdin:\n%s", rec)
	}
	for _, bad := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "CODEX_SANDBOX"} {
		if strings.Contains(rec, bad) {
			t.Errorf("%s reached the CLI", bad)
		}
	}
	args := rec[:strings.Index(rec, "ENV:")]
	if strings.Contains(args, "the owner's words") || strings.Contains(args, "the rules") {
		t.Errorf("input in argv:\n%s", args)
	}
	realWork, _ := filepath.EvalSymlinks(work)
	if !strings.Contains(rec, "PWD:"+realWork+"\n") {
		t.Errorf("not run in home/work:\n%s", rec)
	}
	if left, _ := filepath.Glob(filepath.Join(work, "*")); len(left) != 0 {
		t.Errorf("the schema file is left: %v", left)
	}
}

func TestExecReadsTheFinalMessageAndUsage(t *testing.T) {
	wd, _ := os.Getwd()
	_, bin := fakeCLI(t, "cat >/dev/null; cat "+wd+"/testdata/codex-events.jsonl\n")
	raw, u, err := NewRunner(bin, newHome(t, true), "gpt-x").Run(context.Background(), "s", "in", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"action":"say","tool":"","arguments_json":"","text":"Done."}`; string(raw) != want {
		t.Errorf("raw = %s", raw)
	}
	if u.Model != "gpt-x" || u.InputTokens != 460 || u.OutputTokens != 35 || u.CostKnown || u.CostUsd != 0 {
		t.Errorf("usage = %v", u)
	}
}

func TestExecFailures(t *testing.T) {
	for name, body := range map[string]string{
		"exit":        "echo 'boom: bad thing' >&2; echo second >&2; exit 3\n",
		"turn failed": "echo '{\"type\":\"turn.failed\",\"error\":{\"message\":\"private words\"}}'; echo oops >&2\n",
		"no message":  "echo '{\"type\":\"turn.completed\",\"usage\":{}}'; echo oops >&2\n",
		"not json":    "echo hello; echo oops >&2\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, bin := fakeCLI(t, "cat >/dev/null\n"+body)
			_, _, err := NewRunner(bin, newHome(t, true), "m").Run(context.Background(), "s", "private words", []byte(`{}`))
			if err == nil || connect.CodeOf(err) != connect.CodeInternal {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "private words") || strings.Contains(err.Error(), "secret-tok") {
				t.Errorf("error leaks: %v", err)
			}
		})
	}
	_, bin := fakeCLI(t, "cat >/dev/null\necho 'boom: bad thing' >&2; echo second >&2; exit 3\n")
	_, _, err := NewRunner(bin, newHome(t, true), "m").Run(context.Background(), "s", "i", []byte(`{}`))
	if !strings.Contains(err.Error(), "boom: bad thing") || strings.Contains(err.Error(), "second") {
		t.Errorf("want stderr's first line only: %v", err)
	}
}

func TestNotSignedInIsUnauthenticated(t *testing.T) {
	dir, bin := fakeCLI(t, "")
	os.WriteFile(bin, []byte("#!/bin/sh\ntouch "+dir+"/ran\n"), 0o755)
	_, _, err := NewRunner(bin, newHome(t, false), "m").Run(context.Background(), "s", "i", []byte(`{}`))
	if connect.CodeOf(err) != connect.CodeUnauthenticated || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("err = %v", err)
	}
	if _, e := os.Stat(filepath.Join(dir, "ran")); e == nil {
		t.Error("the CLI ran")
	}
}

func TestLimitIsResourceExhausted(t *testing.T) {
	wd, _ := os.Getwd()
	for name, body := range map[string]string{
		"error event":   "cat " + wd + "/testdata/codex-limit.jsonl\n",
		"error, exit 1": "cat " + wd + "/testdata/codex-limit.jsonl\nexit 1\n",
		"turn failed":   "echo '{\"type\":\"turn.failed\",\"error\":{\"message\":\"You have hit the usage limit for your plan\"}}'\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, bin := fakeCLI(t, "cat >/dev/null\n"+body)
			_, _, err := NewRunner(bin, newHome(t, true), "m").Run(context.Background(), "s", "i", []byte(`{}`))
			if connect.CodeOf(err) != connect.CodeResourceExhausted {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "Try again") {
				t.Errorf("error carries the CLI's text: %v", err)
			}
		})
	}
}

func TestASignInRefusedIsUnauthenticated(t *testing.T) {
	wd, _ := os.Getwd()
	for name, tail := range map[string]string{"exit 0": "", "exit 1": "exit 1\n"} {
		t.Run(name, func(t *testing.T) {
			_, bin := fakeCLI(t, "cat >/dev/null\ncat "+wd+"/testdata/codex-auth.jsonl\n"+tail)
			_, _, err := NewRunner(bin, newHome(t, true), "m").Run(context.Background(), "s", "i", []byte(`{}`))
			if connect.CodeOf(err) != connect.CodeUnauthenticated {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized") {
				t.Errorf("error carries the CLI's text: %v", err)
			}
		})
	}
}

func TestOtherErrorStaysInternal(t *testing.T) {
	_, bin := fakeCLI(t, "cat >/dev/null\necho '{\"type\":\"error\",\"message\":\"the network is down\"}'\n")
	_, _, err := NewRunner(bin, newHome(t, true), "m").Run(context.Background(), "s", "i", []byte(`{}`))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("err = %v", err)
	}
}

const loginScript = `if [ "$1" = "--version" ]; then echo "codex-cli 0.144.6"; exit 0; fi
if [ "$1" = "logout" ]; then echo "$CODEX_HOME" > "$MARK/logout"; exit 0; fi
if [ "$1" = "login" ] && [ "$2" = "status" ]; then [ -f "$CODEX_HOME/auth.json" ]; exit $?; fi
[ "$1" = "login" ] && [ "$2" = "--device-auth" ] || exit 9
printf '\033[94mhttps://auth.example/codex/device\033[0m\n'
printf 'Enter this one-time code: \033[94mABCD-12345\033[0m\n'
printf '\n\033[94mABCD-12345\033[0m\n'
while [ ! -f "$MARK/approved" ]; do sleep 0.05; done
mkdir -p "$CODEX_HOME"; echo '{}' > "$CODEX_HOME/auth.json"
`

// markedCLI is a fake whose MARK directory (its own temp dir) the test controls.
func markedCLI(t *testing.T, body string) (mark, bin string) {
	t.Helper()
	dir, bin := fakeCLI(t, "")
	os.WriteFile(bin, []byte("#!/bin/sh\nMARK='"+dir+"'\n"+body), 0o755)
	return dir, bin
}

func TestDeviceLoginShowsTheCode(t *testing.T) {
	mark, bin := markedCLI(t, loginScript)
	home := filepath.Join(newHome(t, false), "agent")
	l := NewLogin(bin, home)
	ctx := context.Background()
	st, err := l.Status(ctx)
	if err != nil || st.SignedIn || st.CliVersion != "codex-cli 0.144.6" {
		t.Fatalf("status = %v, %v", st, err)
	}
	start, err := l.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if start.Url != "https://auth.example/codex/device" || start.Code != "ABCD-12345" || start.NeedsCode {
		t.Fatalf("start = %v", start)
	}
	os.WriteFile(filepath.Join(mark, "approved"), nil, 0o600)
	if err := l.Finish(ctx, "ignored"); err != nil {
		t.Fatal(err)
	}
	if st, _ = l.Status(ctx); !st.SignedIn {
		t.Error("not signed in after the login ended")
	}
	if err := l.Finish(ctx, ""); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("finish with nothing pending: %v", err)
	}
}

func TestFinishFailsWhenTheLoginExitsNonZero(t *testing.T) {
	_, bin := fakeCLI(t, "echo https://x.example/d\necho ABCD-12345\nsleep 0.2\necho 'expired secret-tok' >&2\nexit 1\n")
	l := NewLogin(bin, filepath.Join(newHome(t, false), "agent"))
	if _, err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := l.Finish(context.Background(), "")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || strings.Contains(err.Error(), "secret-tok") {
		t.Fatalf("err = %v", err)
	}
}

func TestLogoutRunsCodexLogout(t *testing.T) {
	mark, bin := markedCLI(t, loginScript)
	home := newHome(t, true)
	l := NewLogin(bin, home)
	if err := l.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(mark, "logout"))); got != filepath.Join(home, ".codex") {
		t.Errorf("logout CODEX_HOME = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); err == nil {
		t.Error("home/.codex still there")
	}
	if err := l.Logout(context.Background()); err != nil {
		t.Errorf("logout when signed out: %v", err)
	}
}

func TestTheCodeCountsOnlyWhenItsLineIsComplete(t *testing.T) {
	_, bin := fakeCLI(t, "echo https://x.example/d\nprintf 'ABCD-12'\nsleep 0.4\necho 345\n")
	l := NewLogin(bin, filepath.Join(newHome(t, false), "agent"))
	start, err := l.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if start.Code != "ABCD-12345" {
		t.Errorf("code = %q", start.Code)
	}
}

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

func TestASecondStartKillsThePendingLogin(t *testing.T) {
	dir, bin := fakeCLI(t, "")
	pids := filepath.Join(dir, "pids")
	os.WriteFile(bin, []byte("#!/bin/sh\n[ \"$1\" = login ] || exit 0\necho $$ >> "+pids+"\necho https://x.example/y\necho ABCD-12345\nsleep 30\n"), 0o755)
	l := NewLogin(bin, filepath.Join(newHome(t, false), "agent"))
	t.Cleanup(func() { l.Logout(context.Background()) })
	for i := 0; i < 2; i++ {
		if _, err := l.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got := pidsOf(t, pids)
	if len(got) != 2 {
		t.Fatalf("pids = %v", got)
	}
	waitGone(t, got[0])
	if syscall.Kill(got[1], 0) != nil {
		t.Error("the new login is not running")
	}
	l.Logout(context.Background())
	waitGone(t, got[1])
}

func TestACancelledStartAndAFinishTimeoutKillTheLogin(t *testing.T) {
	dir, bin := fakeCLI(t, "")
	pids := filepath.Join(dir, "pids")
	os.WriteFile(bin, []byte("#!/bin/sh\necho $$ >> "+pids+"\nsleep 30\n"), 0o755)
	l := NewLogin(bin, filepath.Join(newHome(t, false), "agent"))

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	if _, err := l.Start(ctx); err == nil {
		t.Fatal("Start without a link succeeded")
	}
	waitFile(t, pids)
	waitGone(t, pidsOf(t, pids)[0])

	os.WriteFile(bin, []byte("#!/bin/sh\necho $$ >> "+pids+"\necho https://x.example/y\necho ABCD-12345\nsleep 30\n"), 0o755)
	if _, err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	shorten(t, &finishWait, 300*time.Millisecond)
	if err := l.Finish(context.Background(), ""); err == nil {
		t.Fatal("Finish of a login that never ends succeeded")
	}
	got := pidsOf(t, pids)
	if len(got) != 2 {
		t.Fatalf("pids = %v", got)
	}
	waitGone(t, got[1])
}

func TestExecKillsTheCLIAndItsChildOnTimeout(t *testing.T) {
	dir, bin := fakeCLI(t, "")
	pids := filepath.Join(dir, "pids")
	os.WriteFile(bin, []byte("#!/bin/sh\necho $$ > "+pids+"\nsleep 30 &\necho $! >> "+pids+"\nsleep 30\n"), 0o755)
	shorten(t, &runTimeout, 1000*time.Millisecond)
	shorten(t, &waitDelay, 300*time.Millisecond)
	start := time.Now()
	_, _, err := NewRunner(bin, newHome(t, true), "m").Run(context.Background(), "s", "i", []byte(`{}`))
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
	waitGone(t, got[1])
}

// A Start or a Logout while a Finish waits ends that login, and the Finish with it, promptly.
func TestAStartOrLogoutDuringFinishEndsTheWait(t *testing.T) {
	for name, interrupt := range map[string]func(*Login){
		"start":  func(l *Login) { l.Start(context.Background()) },
		"logout": func(l *Login) { l.Logout(context.Background()) },
	} {
		t.Run(name, func(t *testing.T) {
			dir, bin := fakeCLI(t, "")
			pids := filepath.Join(dir, "pids")
			os.WriteFile(bin, []byte("#!/bin/sh\n[ \"$1\" = login ] || exit 0\necho $$ >> "+pids+"\necho https://x.example/y\necho ABCD-12345\nsleep 30\n"), 0o755)
			l := NewLogin(bin, filepath.Join(newHome(t, false), "agent"))
			t.Cleanup(func() { l.Logout(context.Background()) })
			if _, err := l.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			res := make(chan error, 1)
			go func() { res <- l.Finish(context.Background(), "") }()
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

// A second Finish while one waits finds nothing to finish.
func TestASecondFinishFindsNothingPending(t *testing.T) {
	_, bin := fakeCLI(t, "[ \"$1\" = login ] || exit 0\necho https://x.example/y\necho ABCD-12345\nsleep 30\n")
	l := NewLogin(bin, filepath.Join(newHome(t, false), "agent"))
	t.Cleanup(func() { l.Logout(context.Background()) })
	if _, err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	go l.Finish(context.Background(), "")
	time.Sleep(300 * time.Millisecond)
	if err := l.Finish(context.Background(), ""); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("second finish: %v", err)
	}
}
