package salsa20

import (
	"context"
	"encoding/base64"

	"github.com/primandproper/platform-go/cryptography/encryption"
	"github.com/primandproper/platform-go/observability/keys"

	"golang.org/x/crypto/salsa20"
)

func (e *salsa20Impl) Decrypt(ctx context.Context, content string) (string, error) {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.LengthKey, len(content))

	raw, err := base64.URLEncoding.DecodeString(content)
	if err != nil {
		return "", op.Error(err, "decoding ciphered content")
	}

	if len(raw) < nonceSize {
		return "", op.Error(encryption.ErrMalformedCiphertext, "ciphertext too short for nonce")
	}

	nonce, ciphered := raw[:nonceSize], raw[nonceSize:]

	out := make([]byte, len(ciphered))
	salsa20.XORKeyStream(out, ciphered, nonce, &e.key)

	return string(out), nil
}
