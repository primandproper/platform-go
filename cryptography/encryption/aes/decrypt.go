package aes

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"

	"github.com/primandproper/platform-go/v4/cryptography/encryption"
	"github.com/primandproper/platform-go/v4/observability/keys"
)

func (e *aesImpl) Decrypt(ctx context.Context, content string) (string, error) {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.LengthKey, len(content))

	ciphered, err := base64.URLEncoding.DecodeString(content)
	if err != nil {
		return "", op.Error(err, "decoding ciphered text")
	}

	aesBlock, err := aes.NewCipher(e.key[:])
	if err != nil {
		return "", op.Error(err, "creating aes cipher")
	}

	gcmInstance, err := cipher.NewGCM(aesBlock)
	if err != nil {
		return "", op.Error(err, "creating gcm instance")
	}

	nonceSize := gcmInstance.NonceSize()
	if len(ciphered) < nonceSize {
		return "", op.Error(encryption.ErrMalformedCiphertext, "ciphertext too short for nonce")
	}

	nonce, cipheredText := ciphered[:nonceSize], ciphered[nonceSize:]

	originalText, err := gcmInstance.Open(nil, nonce, cipheredText, nil)
	if err != nil {
		return "", op.Error(err, "decrypting ciphered text")
	}

	return string(originalText), nil
}
