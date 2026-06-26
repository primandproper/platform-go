package aes

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"

	"github.com/primandproper/platform-go/observability"
)

func (e *aesImpl) Encrypt(ctx context.Context, content string) (string, error) {
	_, span := e.tracer.StartSpan(ctx)
	defer span.End()

	logger := e.logger.WithValue("content", content)

	aesBlock, err := aes.NewCipher(e.key[:])
	if err != nil {
		return "", observability.PrepareAndLogError(err, logger, span, "creating aes cipher")
	}

	gcmInstance, err := cipher.NewGCM(aesBlock)
	if err != nil {
		return "", observability.PrepareAndLogError(err, logger, span, "creating gcm instance")
	}

	nonce := make([]byte, gcmInstance.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", observability.PrepareAndLogError(err, logger, span, "generating nonce")
	}

	cipheredText := gcmInstance.Seal(nonce, nonce, []byte(content), nil)

	return base64.URLEncoding.EncodeToString(cipheredText), nil
}
