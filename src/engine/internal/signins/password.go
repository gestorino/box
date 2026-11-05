package signins

import (
	"crypto/rand"
	"math/big"
)

// The password the box generates for an account: 16 characters, at least one lower-case letter, one
// upper-case letter and one digit, and exactly one "!" — what nearly every sign-up form accepts, with
// no site's rules to go by.
const (
	passwordLength = 16
	lower          = "abcdefghijklmnopqrstuvwxyz"
	upper          = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digits         = "0123456789"
)

func newPassword() string {
	p := []byte{pickChar(lower), pickChar(upper), pickChar(digits), '!'}
	for len(p) < passwordLength {
		p = append(p, pickChar(lower+upper+digits))
	}
	for i := len(p) - 1; i > 0; i-- { // Fisher–Yates, so the four aren't always first
		j := randIndex(i + 1)
		p[i], p[j] = p[j], p[i]
	}
	return string(p)
}

func pickChar(set string) byte { return set[randIndex(len(set))] }

func randIndex(n int) int {
	i, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err) // crypto/rand never fails on a supported system
	}
	return int(i.Int64())
}
