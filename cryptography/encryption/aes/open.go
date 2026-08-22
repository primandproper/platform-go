package aes

import (
	"context"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
)

// Open reverses Seal.
//
// Every way of failing to authenticate collapses to
// encryption.ErrAuthenticationFailed: a tampered ciphertext, the wrong key,
// and associated data that does not match all produce the same GCM failure,
// and reporting which one it was would answer a question only an attacker is
// asking.
func (e *Cipher) Open(ctx context.Context, ciphertext, associatedData []byte) ([]byte, error) {
	_, op := e.o11y.Begin(ctx, observability.WithValue(keys.LengthKey, len(ciphertext)))
	defer op.End()

	nonceSize := e.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, op.Error(encryption.ErrMalformedCiphertext, "ciphertext too short for nonce")
	}

	nonce, body := ciphertext[:nonceSize], ciphertext[nonceSize:]

	plaintext, err := e.aead.Open(nil, nonce, body, associatedData)
	if err != nil {
		return nil, op.Error(encryption.ErrAuthenticationFailed, "opening ciphertext")
	}

	return plaintext, nil
}
