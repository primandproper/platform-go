package salsa20

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"

	"github.com/primandproper/platform-go/observability/keys"

	"golang.org/x/crypto/salsa20"
)

func (e *salsa20Impl) Encrypt(ctx context.Context, content string) (string, error) {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.LengthKey, len(content))

	out := make([]byte, nonceSize+len(content))
	nonce := out[:nonceSize]
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", op.Error(err, "generating nonce")
	}

	salsa20.XORKeyStream(out[nonceSize:], []byte(content), nonce, &e.key)

	return base64.URLEncoding.EncodeToString(out), nil
}
