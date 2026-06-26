package aes

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"

	"github.com/primandproper/platform-go/observability"
)

func (e *aesImpl) Decrypt(ctx context.Context, content string) (string, error) {
	_, span := e.tracer.StartSpan(ctx)
	defer span.End()

	logger := e.logger.WithValue("content", content)

	ciphered, err := base64.URLEncoding.DecodeString(content)
	if err != nil {
		return "", observability.PrepareAndLogError(err, logger, span, "decoding ciphered text")
	}

	aesBlock, err := aes.NewCipher(e.key[:])
	if err != nil {
		return "", observability.PrepareAndLogError(err, logger, span, "creating aes cipher")
	}

	gcmInstance, err := cipher.NewGCM(aesBlock)
	if err != nil {
		return "", observability.PrepareAndLogError(err, logger, span, "creating gcm instance")
	}

	nonceSize := gcmInstance.NonceSize()
	nonce, cipheredText := ciphered[:nonceSize], ciphered[nonceSize:]

	originalText, err := gcmInstance.Open(nil, nonce, cipheredText, nil)
	if err != nil {
		return "", observability.PrepareAndLogError(err, logger, span, "decrypting ciphered text")
	}

	return string(originalText), nil
}
