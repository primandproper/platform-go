package salsa20

import (
	"context"
	"encoding/base64"

	"github.com/primandproper/platform-go/observability"

	"golang.org/x/crypto/salsa20"
)

func (e *salsa20Impl) Decrypt(ctx context.Context, content string) (string, error) {
	_, span := e.tracer.StartSpan(ctx)
	defer span.End()

	logger := e.logger.WithValue("content", content)

	ciphered, err := base64.URLEncoding.DecodeString(content)
	if err != nil {
		return "", observability.PrepareAndLogError(err, logger, span, "decoding ciphered content")
	}

	out := make([]byte, len(ciphered))
	salsa20.XORKeyStream(out, ciphered, []byte{0, 0, 0, 0, 0, 0, 0, 0}, &e.key)

	return string(out), nil
}
