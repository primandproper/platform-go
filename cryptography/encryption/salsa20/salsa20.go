package salsa20

import (
	"github.com/primandproper/platform-go/v6/cryptography/encryption"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/tracing"
)

const name = "salsa20_encryptor"

// nonceSize is the length in bytes of the XSalsa20 nonce generated per message.
// NaCl secretbox (XSalsa20-Poly1305) uses a 24-byte nonce, which is long enough
// that randomly generated nonces have a negligible risk of collision.
const nonceSize = 24

// salsa20Impl is the standard EncryptorDecryptor implementation. It uses NaCl
// secretbox (XSalsa20-Poly1305) for authenticated encryption.
type salsa20Impl struct {
	o11y observability.Observer
	key  [32]byte
}

func NewEncryptorDecryptor(tracerProvider tracing.TracerProvider, logger logging.Logger, key []byte) (encryption.EncryptorDecryptor, error) {
	if len(key) != 32 {
		return nil, encryption.ErrIncorrectKeyLength
	}

	var key32 [32]byte
	copy(key32[:], key)

	return &salsa20Impl{
		o11y: observability.NewObserver(name, logger, tracerProvider),
		key:  key32,
	}, nil
}
