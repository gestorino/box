package keyfile

import (
	"strings"
	"testing"
)

const testKey = "sk-or-v1-0123456789abcdefa1b2"

func TestMaskedShowsThePrefixAndTheLastFour(t *testing.T) {
	for _, c := range []struct{ key, prefix, want string }{
		{testKey, "sk-or-", "sk-or-…a1b2"},
		{"sk-proj-0123456789abcdefc3d4", "sk-", "sk-…c3d4"},
		{"0123456789abcdefe5f6", "sk-", "…e5f6"}, // no prefix to show
	} {
		if got := Masked(c.key, c.prefix); got != c.want {
			t.Fatalf("Masked(%q, %q) = %q, want %q", c.key, c.prefix, got, c.want)
		}
	}
	if got := Masked("sk-ab", "sk-"); strings.Contains(got, "ab") {
		t.Fatalf("a short key shows whole: %q", got)
	}
}

func TestWellFormedIsOneLineOfPrintableCharacters(t *testing.T) {
	for _, ok := range []string{testKey, "x", strings.Repeat("a", 512)} {
		if !WellFormed(ok) {
			t.Fatalf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "sk a", "sk\ta", "sk\na", "sk\x00a", "ключ", strings.Repeat("a", 513)} {
		if WellFormed(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
}
