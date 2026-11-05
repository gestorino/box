package seal

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func testKey(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func TestSealRoundTripsAndATamperedBoxFails(t *testing.T) {
	s, err := NewKey(testKey(t, 32))
	if err != nil {
		t.Fatal(err)
	}
	box, err := s.Seal([]byte("hunter2"), []byte("n"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(box, []byte("hunter2")) {
		t.Fatal("plaintext visible in the box")
	}
	if got, err := s.Open(box, []byte("n")); err != nil || string(got) != "hunter2" {
		t.Fatalf("open = %q %v", got, err)
	}
	other, _ := s.Seal([]byte("hunter2"), []byte("n"))
	if bytes.Equal(box, other) {
		t.Fatal("nonce reused")
	}
	box[len(box)-1] ^= 1
	if _, err := s.Open(box, []byte("n")); err == nil {
		t.Fatal("tampered box opened")
	}
	if _, err := s.Open([]byte{1, 2}, nil); err == nil {
		t.Fatal("short box opened")
	}
	wrong, _ := NewKey(testKey(t, 32))
	good, _ := s.Seal([]byte("x"), nil)
	if _, err := wrong.Open(good, nil); err == nil {
		t.Fatal("opened under another key")
	}
}

func TestAKeyThatIsNot32BytesIsRefused(t *testing.T) {
	for _, k := range [][]byte{nil, testKey(t, 16), testKey(t, 33)} {
		_, err := NewKey(k)
		if err == nil {
			t.Fatalf("key of %d bytes accepted", len(k))
		}
		if len(k) > 0 && strings.Contains(err.Error(), string(k)) {
			t.Fatalf("error echoes the key: %v", err)
		}
	}
}

func TestOpenRefusesAnotherName(t *testing.T) {
	s, _ := NewKey(testKey(t, 32))
	box, err := s.Seal([]byte("v"), []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(box, []byte("b")); err == nil || err.Error() != "sealed value does not open" {
		t.Fatalf("opened under another name: %v", err)
	}
	if got, err := s.Open(box, []byte("a")); err != nil || string(got) != "v" {
		t.Fatalf("open = %q %v", got, err)
	}
}

func TestNewKeyNeeds32Bytes(t *testing.T) {
	raw := bytes.Repeat([]byte{7}, 31)
	_, err := NewKey(raw)
	if err == nil {
		t.Fatal("31 bytes accepted")
	}
	if strings.Contains(err.Error(), string(raw)) {
		t.Fatalf("error echoes the key: %v", err)
	}
	if _, err := NewKey(bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
}
