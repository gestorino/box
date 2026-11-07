package main

import (
	"bytes"
	"strings"
	"testing"
)

func runCmd(args []string, uid int) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, uid, nil, &out, &errb)
	return code, out.String(), errb.String()
}

func TestRefusesWithoutRoot(t *testing.T) {
	code, out, errs := runCmd([]string{"create"}, 1000)
	if code != 1 || out != "" || !strings.Contains(errs, "root") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
	}
}

func TestUnknownSubcommand(t *testing.T) {
	code, _, errs := runCmd([]string{"frobnicate"}, 0)
	if code != 1 || !strings.Contains(errs, `unknown command "frobnicate"`) {
		t.Fatalf("code %d, stderr %q", code, errs)
	}
}

func TestUsageNamesEverySubcommand(t *testing.T) {
	code, _, errs := runCmd(nil, 0)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	for _, s := range []string{"create", "resume", "recovery-code", "restore", "install-build"} {
		if !strings.Contains(errs, s) {
			t.Errorf("usage does not name %q: %s", s, errs)
		}
	}
}

func TestRestoreNeedsFrom(t *testing.T) {
	for _, c := range []string{"restore", "install-build"} {
		code, _, errs := runCmd([]string{c}, 0)
		if code != 1 || !strings.Contains(errs, "-from") {
			t.Errorf("%s: code %d, stderr %q", c, code, errs)
		}
	}
}

func TestVersionNeedsNoRoot(t *testing.T) {
	code, out, _ := runCmd([]string{"-version"}, 1000)
	if code != 0 || !strings.Contains(out, "dev") {
		t.Fatalf("code %d, stdout %q", code, out)
	}
}
