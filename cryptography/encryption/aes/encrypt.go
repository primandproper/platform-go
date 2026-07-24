package aes

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"

	"github.com/primandproper/platform-go/v6/observability/keys"
)

func (e *aesImpl) Encrypt(ctx context.Context, content string) (string, error) {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.LengthKey, len(content))

	aesBlock, err := aes.NewCipher(e.key[:])
	if err != nil {
		return "", op.Error(err, "creating aes cipher")
	}

	gcmInstance, err := cipher.NewGCM(aesBlock)
	if err != nil {
		return "", op.Error(err, "creating gcm instance")
	}

	nonce := make([]byte, gcmInstance.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", op.Error(err, "generating nonce")
	}

	cipheredText := gcmInstance.Seal(nonce, nonce, []byte(content), nil)

	return base64.URLEncoding.EncodeToString(cipheredText), nil
}
