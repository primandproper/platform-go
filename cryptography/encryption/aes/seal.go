package aes

import (
	"context"
	"io"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
)

// Seal encrypts plaintext, authenticating associatedData alongside it.
//
// The output is nonce || ciphertext || tag, unencoded. The previous surface
// base64'd its result because it returned a string; a []byte surface has
// nothing to escape, and a caller that needs text can encode once at the
// boundary that needs it rather than paying a third in size at rest.
func (e *Cipher) Seal(ctx context.Context, plaintext, associatedData []byte) ([]byte, error) {
	_, op := e.o11y.Begin(ctx, observability.WithValue(keys.LengthKey, len(plaintext)))
	defer op.End()

	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(e.random, nonce); err != nil {
		return nil, op.Error(err, "generating nonce")
	}

	return e.aead.Seal(nonce, nonce, plaintext, associatedData), nil
}
