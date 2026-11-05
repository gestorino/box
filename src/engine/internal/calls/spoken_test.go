package calls

import (
	"encoding/binary"
	"reflect"
	"testing"

	"concierge/engine/internal/verify"
)

func s16(samples ...int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

func TestULawRoundTrip(t *testing.T) {
	in := []int16{0, 1000, -1000, 32767, -32768, 12345, -12345, 50, -50}
	u := toULaw(s16(in...))
	if len(u) != len(in) {
		t.Fatalf("%d bytes for %d samples", len(u), len(in))
	}
	if u[0] != 0xFF || u[4] != 0x00 { // G.711: silence is 0xFF, the loudest negative 0x00
		t.Fatalf("toULaw(0)=%#x toULaw(-32768)=%#x", u[0], u[4])
	}
	out := fromULaw(u)
	for i, want := range in {
		got := int16(binary.LittleEndian.Uint16(out[2*i:]))
		diff, tol := int(got)-int(want), int(want)/16
		if tol < 0 {
			tol = -tol
		}
		if diff < 0 {
			diff = -diff
		}
		if diff > tol+8 {
			t.Errorf("%d → %#x → %d", want, u[i], got)
		}
	}
	if len(toULaw([]byte{1})) != 0 { // half a sample is no sample
		t.Fatal("odd byte encoded")
	}
}

func TestSpokenNumbers(t *testing.T) {
	for _, c := range []struct {
		lang, text string
		want       []int
	}{
		{"en", "forty two", []int{42}}, {"en", "Forty-two.", []int{42}}, {"en", "forty… forty-two", []int{40, 42}},
		{"en", "it's 42", []int{42}}, {"en", "nineteen", []int{19}}, {"en", "4-7-2", []int{4, 7, 2}},
		{"en", "forty-two, no, forty-one", []int{42, 41}},
		{"pt", "quarenta e dois", []int{42}}, {"pt", "30 e 2", []int{32}}, {"pt", "dezanove", []int{19}}, {"pt", "dezenove", []int{19}},
		{"pt", "É quarenta e dois.", []int{42}}, {"pt", "forty two", []int{42}},
		{"ru", "сорок два", []int{42}}, {"ru", "Сорок два!", []int{42}}, {"ru", "трин… сорок два", []int{42}}, {"ru", "одиннадцать", []int{11}},
		{"es", "cuarenta y dos", []int{42}}, {"es", "veintidós", []int{22}}, {"es", "treinta y 2", []int{32}},
		{"en", "", nil}, {"en", "no idea", nil}, {"en", "twenty and", []int{20}},
	} {
		if got := spokenNumbers(c.lang, c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %q: %v, want %v", c.lang, c.text, got, c.want)
		}
	}
}

func TestAnsweredAloud(t *testing.T) {
	c := verify.Challenge{A: 23, B: 19}
	for _, x := range []struct {
		lang, text string
		want       bool
	}{
		{"en", "forty… forty-two", true}, {"en", "forty-two, no, forty-one", false}, {"ru", "сорок два", true},
		{"pt", "30 e 2", false}, {"en", "", false}, {"en", "42", true}, {"es", "cuarenta y dos", true},
	} {
		if got := answeredAloud(c, x.lang, x.text); got != x.want {
			t.Errorf("%s %q: %v", x.lang, x.text, got)
		}
	}
}

func TestLanguages(t *testing.T) {
	if l, ok := languageOf("ru"); !ok || l.Name != "Русский" {
		t.Fatal(l, ok)
	}
	if _, ok := languageOf("fr"); ok {
		t.Fatal("fr")
	}
	if Languages[0].Code != "en" {
		t.Fatal("English is the default, first")
	}
}
