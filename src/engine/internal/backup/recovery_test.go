package backup

import (
	"bytes"
	"crypto/rand"
	"errors"
	"regexp"
	"strings"
	"testing"
)

func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestCodeFormat(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){5}$`)
	a, err := NewCode()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewCode()
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Fatalf("code does not match the format")
	}
	if a == b {
		t.Fatal("two codes are equal")
	}
}

func TestLockUnlockRoundTrip(t *testing.T) {
	key := randomKey(t)
	code, _ := NewCode()
	locked, err := Lock(key, code)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unlock(locked, code)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) {
		t.Fatal("unlocked key differs")
	}
}

func TestUnlockForgivesTyping(t *testing.T) {
	key := randomKey(t)
	// A code with 0 and 1 in it, so the look-alike letters matter.
	code := "0123-4567-89AB-CDEF-GHJK-MNPQ"
	locked, err := Lock(key, code)
	if err != nil {
		t.Fatal(err)
	}
	for _, typed := range []string{
		strings.ToLower(code),
		strings.ReplaceAll(code, "-", " "),
		"O123-4567-89AB-CDEF-GHJK-MNPQ",
		"0I23-4567-89AB-CDEF-GHJK-MNPQ",
		"0l23-4567-89AB-CDEF-GHJK-MNPQ",
		"  " + code + "\n",
	} {
		got, err := Unlock(locked, typed)
		if err != nil || !bytes.Equal(got, key) {
			t.Fatalf("%q did not unlock: %v", typed, err)
		}
	}
}

func TestUnlockWrongCode(t *testing.T) {
	key := randomKey(t)
	code, _ := NewCode()
	other, _ := NewCode()
	locked, err := Lock(key, code)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{other, "0000-0000", "", code + "AAAA"} {
		if _, err := Unlock(locked, bad); !errors.Is(err, ErrWrongCode) {
			t.Fatalf("%q: got %v, want ErrWrongCode", bad, err)
		}
	}
}

func TestLockRefusesAKeyNot32Bytes(t *testing.T) {
	code, _ := NewCode()
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := Lock(make([]byte, n), code); err == nil {
			t.Fatalf("a %d-byte key was locked", n)
		}
	}
}

func TestLockIsSaltedEachTime(t *testing.T) {
	key := randomKey(t)
	code, _ := NewCode()
	a, err := Lock(key, code)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Lock(key, code)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two locks are identical")
	}
	for _, l := range [][]byte{a, b} {
		if got, err := Unlock(l, code); err != nil || !bytes.Equal(got, key) {
			t.Fatalf("lock does not unlock: %v", err)
		}
	}
}
