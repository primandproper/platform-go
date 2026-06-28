package salsa20

import (
	"github.com/primandproper/platform-go/cryptography/encryption"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"
)

// salsa20Impl is the standard EncryptorDecryptor implementation.
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
		o11y: observability.NewObserver("encryptor", logger, tracerProvider),
		key:  key32,
	}, nil
}
