package aes

import (
	"github.com/primandproper/platform-go/cryptography/encryption"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"
)

const name = "encryptor"

// aesImpl is the standard EncryptorDecryptor implementation.
type aesImpl struct {
	o11y observability.Observer
	key  [32]byte
}

func NewEncryptorDecryptor(tracerProvider tracing.TracerProvider, logger logging.Logger, key []byte) (encryption.EncryptorDecryptor, error) {
	if len(key) != 32 {
		return nil, encryption.ErrIncorrectKeyLength
	}

	var key32 [32]byte
	copy(key32[:], key)

	return &aesImpl{
		o11y: observability.NewObserver(name, logger, tracerProvider),
		key:  key32,
	}, nil
}
