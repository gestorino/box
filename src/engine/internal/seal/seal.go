package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// Sealer encrypts secrets at rest (AES-256-GCM): secretd seals every secret under the box's master
// key (TPM-encrypted, never on disk in the clear). A database copied off the box is useless without its key.
type Sealer struct{ aead cipher.AEAD }

// NewKey takes the key as exactly 32 raw bytes. Errors never echo the key.
func NewKey(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, errors.New("sealing key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal returns nonce‖ciphertext with a fresh random nonce. ad is authenticated, not stored: the
// box opens only under the same ad (the value's name), so one moved to another row does not open.
func (s *Sealer) Seal(plain, ad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, ad), nil
}

// Open reverses Seal; a tampered or foreign box fails authentication.
func (s *Sealer) Open(box, ad []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(box) < n {
		return nil, errors.New("sealed value too short")
	}
	plain, err := s.aead.Open(nil, box[:n], box[n:], ad)
	if err != nil {
		return nil, errors.New("sealed value does not open")
	}
	return plain, nil
}
