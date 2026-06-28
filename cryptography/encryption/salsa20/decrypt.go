package salsa20

import (
	"context"
	"encoding/base64"

	"github.com/primandproper/platform-go/observability/keys"

	"golang.org/x/crypto/salsa20"
)

func (e *salsa20Impl) Decrypt(ctx context.Context, content string) (string, error) {
	_, op := e.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.LengthKey, len(content))

	ciphered, err := base64.URLEncoding.DecodeString(content)
	if err != nil {
		return "", op.Error(err, "decoding ciphered content")
	}

	out := make([]byte, len(ciphered))
	salsa20.XORKeyStream(out, ciphered, []byte{0, 0, 0, 0, 0, 0, 0, 0}, &e.key)

	return string(out), nil
}
