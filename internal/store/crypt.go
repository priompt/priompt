package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// encPrefix marks an encrypted column value. Plaintext values (written before a
// key was configured) lack it and are returned as-is, so encrypted and plaintext
// rows coexist during rollout — no migration needed to turn encryption on.
//
// ponytail: a plaintext template literally beginning with "enc1:", written while
// encryption was OFF and later read with a key ON, would be mis-detected as
// ciphertext and fail to decrypt. Vanishingly unlikely for prompt text; move the
// marker to its own column if it ever bites.
const encPrefix = "enc1:"

// loadCipher builds the column cipher from PRIOMPT_ENCRYPTION_KEY (base64 of a
// 32-byte key) for AES-256-GCM. Unset -> nil (plaintext storage, unchanged).
// Set but invalid -> error: if the operator asked for encryption, never silently
// fall back to storing plaintext.
func loadCipher() (cipher.AEAD, error) {
	raw := os.Getenv("PRIOMPT_ENCRYPTION_KEY")
	if raw == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("PRIOMPT_ENCRYPTION_KEY: not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("PRIOMPT_ENCRYPTION_KEY: want 32 bytes base64-encoded, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// seal encrypts a column value — a random nonce is prepended to the ciphertext,
// the whole thing base64'd and marked. Passthrough when no key is configured.
// Non-deterministic (fresh nonce each call), which is fine: identity and dedup
// key off version_hash, computed over plaintext, never the stored column.
func (s *Store) seal(plain string) string {
	if s.aead == nil {
		return plain
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	ct := s.aead.Seal(nonce, nonce, []byte(plain), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(ct)
}

// open reverses seal. A value without the marker is plaintext (a pre-key row, or
// no key configured) and returned unchanged.
func (s *Store) open(stored string) (string, error) {
	if s.aead == nil || !strings.HasPrefix(stored, encPrefix) {
		return stored, nil
	}
	ct, err := base64.StdEncoding.DecodeString(stored[len(encPrefix):])
	if err != nil {
		return "", err
	}
	n := s.aead.NonceSize()
	if len(ct) < n {
		return "", errors.New("encrypted column too short")
	}
	plain, err := s.aead.Open(nil, ct[:n], ct[n:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt column: %w", err)
	}
	return string(plain), nil
}
