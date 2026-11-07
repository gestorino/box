package cliproc

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
)

func TestEnvDropsPrefixesAndSetsWin(t *testing.T) {
	t.Setenv("VENDOR_KEY", "x")
	t.Setenv("HOME", "/elsewhere")
	t.Setenv("KEEP_ME", "y")
	env := Env([]string{"VENDOR_"}, "HOME=/mine")
	var homes int
	for _, kv := range env {
		if strings.HasPrefix(kv, "VENDOR_") {
			t.Errorf("%s passed", kv)
		}
		if strings.HasPrefix(kv, "HOME=") {
			homes++
			if kv != "HOME=/mine" {
				t.Errorf("HOME = %s", kv)
			}
		}
	}
	if homes != 1 {
		t.Errorf("%d HOME entries", homes)
	}
	if !strings.Contains(strings.Join(env, "\n"), "KEEP_ME=y") {
		t.Error("an unrelated variable was dropped")
	}
}

func TestFailKeepsStderrsFirstLineAndHidesSecrets(t *testing.T) {
	err := Fail("it failed", "bad tok-123 here\nsecond\n", nil, "tok-123")
	if connect.CodeOf(err) != connect.CodeInternal || strings.Contains(err.Error(), "tok-123") || strings.Contains(err.Error(), "second") || !strings.Contains(err.Error(), "bad") {
		t.Errorf("err = %v", err)
	}
}

func TestSessionAwaitsTerminatedLinesAndKillsTheGroup(t *testing.T) {
	r, w, _ := os.Pipe()
	pidr, pidw, _ := os.Pipe()
	cmd := exec.Command("/bin/sh", "-c", "echo $$ >&2; printf 'par'; sleep 0.3; echo tial; sleep 30 & sleep 30")
	cmd.Stdout, cmd.Stderr = w, pidw
	OwnGroup(cmd, time.Second)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	pidw.Close()
	s := NewSession(cmd, r)
	// "par" alone must not satisfy a terminated-line check.
	out, ok := s.Await(context.Background(), 5*time.Second, func(t string) bool { return strings.HasSuffix(t, "partial\n") })
	if !ok || out != "partial\n" {
		t.Fatalf("out = %q, %v", out, ok)
	}
	b := make([]byte, 32)
	n, _ := pidr.Read(b)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b[:n])))
	begun := time.Now()
	s.Kill()
	s.Kill() // idempotent
	if time.Since(begun) > time.Second {
		t.Errorf("Kill took %v", time.Since(begun))
	}
	if syscall.Kill(-pid, 0) == nil {
		t.Error("the group is still alive")
	}
}

func TestSlotReplacesAndClaimsOnce(t *testing.T) {
	start := func() *Session {
		r, w, _ := os.Pipe()
		cmd := exec.Command("/bin/sleep", "30")
		cmd.Stdout = w
		OwnGroup(cmd, time.Second)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		w.Close()
		return NewSession(cmd, r)
	}
	var p Slot
	if p.Claim() != nil {
		t.Fatal("claimed from an empty slot")
	}
	a, b := start(), start()
	p.Set(a)
	p.Set(b)
	select {
	case <-a.Exited():
	case <-time.After(2 * time.Second):
		t.Fatal("the replaced session lives")
	}
	if p.Claim() != b || p.Claim() != nil {
		t.Fatal("claim is not once")
	}
	p.Release(b)
	if p.Claim() != nil {
		t.Fatal("a released session is pending")
	}
}
