package salsa20

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"

	"github.com/primandproper/platform-go/v3/observability/keys"

	"golang.org/x/crypto/nacl/secretbox"
)

func (e *salsa20Impl) Encrypt(ctx context.Context, content string) (string, error) {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.LengthKey, len(content))

	var nonce [nonceSize]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return "", op.Error(err, "generating nonce")
	}

	sealed := secretbox.Seal(nonce[:], []byte(content), &nonce, &e.key)

	return base64.URLEncoding.EncodeToString(sealed), nil
}
