// Package backup makes and restores the box's backups. This file is the recovery code: a copy of
// secretd's master key locked under a code the owner keeps, so a restore onto a fresh install (a
// new TPM) can open the restored secrets.
package backup

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"strings"

	"golang.org/x/crypto/scrypt"

	"concierge/engine/internal/seal"
)

// RecoveryFile is where the locked copy of the master key lives, relative to the root.
const RecoveryFile = "var/lib/concierge-secrets/master-key.recovery"

// ErrWrongCode is every way a recovery code fails to open the locked key.
var ErrWrongCode = errors.New("wrong recovery code")

const (
	codeBytes  = 15
	keyBytes   = 32
	saltBytes  = 16
	scryptN    = 131072
	scryptR    = 8
	scryptP    = 1
	crockford  = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	associated = "concierge-secrets-master recovery v1"
)

var crockfordEnc = base32.NewEncoding(crockford).WithPadding(base32.NoPadding)

// locked is the recovery file's JSON.
type locked struct {
	V    int    `json:"v"`
	KDF  string `json:"kdf"`
	N    int    `json:"n"`
	R    int    `json:"r"`
	P    int    `json:"p"`
	Salt []byte `json:"salt"`
	Box  []byte `json:"box"`
}

// NewCode returns a fresh recovery code: 15 random bytes as six groups of four Crockford base32 characters.
func NewCode() (string, error) {
	b := make([]byte, codeBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := crockfordEnc.EncodeToString(b)
	groups := make([]string, 0, 6)
	for i := 0; i < len(s); i += 4 {
		groups = append(groups, s[i:i+4])
	}
	return strings.Join(groups, "-"), nil
}

// normalize forgives typing (case, dashes, spaces, O for 0, I and L for 1) and returns the code's bytes.
func normalize(code string) ([]byte, error) {
	s := strings.ToUpper(code)
	s = strings.NewReplacer("-", "", " ", "", "\t", "", "\n", "", "\r", "", "O", "0", "I", "1", "L", "1").Replace(s)
	b, err := crockfordEnc.DecodeString(s)
	if err != nil || len(b) != codeBytes {
		return nil, ErrWrongCode
	}
	return b, nil
}

func derive(code, salt []byte, n, r, p int) ([]byte, error) {
	return scrypt.Key(code, salt, n, r, p, keyBytes)
}

// Lock seals a copy of the 32-byte master key under the code, with a fresh salt.
func Lock(masterKey []byte, code string) ([]byte, error) {
	if len(masterKey) != keyBytes {
		return nil, errors.New("master key must be 32 bytes")
	}
	raw, err := normalize(code)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	k, err := derive(raw, salt, scryptN, scryptR, scryptP)
	if err != nil {
		return nil, err
	}
	s, err := seal.NewKey(k)
	if err != nil {
		return nil, err
	}
	box, err := s.Seal(masterKey, []byte(associated))
	if err != nil {
		return nil, err
	}
	return json.Marshal(locked{V: 1, KDF: "scrypt", N: scryptN, R: scryptR, P: scryptP, Salt: salt, Box: box})
}

// Unlock returns the master key a Lock sealed, or ErrWrongCode.
func Unlock(lockedKey []byte, code string) ([]byte, error) {
	var l locked
	if err := json.Unmarshal(lockedKey, &l); err != nil {
		return nil, errors.New("recovery file is not readable")
	}
	if l.V != 1 || l.KDF != "scrypt" || l.N != scryptN || l.R != scryptR || l.P != scryptP {
		return nil, errors.New("recovery file is of an unknown kind")
	}
	raw, err := normalize(code)
	if err != nil {
		return nil, err
	}
	k, err := derive(raw, l.Salt, l.N, l.R, l.P)
	if err != nil {
		return nil, err
	}
	s, err := seal.NewKey(k)
	if err != nil {
		return nil, err
	}
	key, err := s.Open(l.Box, []byte(associated))
	if err != nil {
		return nil, ErrWrongCode
	}
	return key, nil
}
