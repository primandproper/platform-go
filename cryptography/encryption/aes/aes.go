package aes

import (
	"github.com/primandproper/platform-go/cryptography/encryption"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"
)

// aesImpl is the standard EncryptorDecryptor implementation.
type aesImpl struct {
	tracer tracing.Tracer
	logger logging.Logger
	key    [32]byte
}

func NewEncryptorDecryptor(tracerProvider tracing.TracerProvider, logger logging.Logger, key []byte) (encryption.EncryptorDecryptor, error) {
	if len(key) != 32 {
		return nil, encryption.ErrIncorrectKeyLength
	}

	var key32 [32]byte
	copy(key32[:], key)

	return &aesImpl{
		logger: logging.NewNamedLogger(logger, "encryptor"),
		tracer: tracing.NewNamedTracer(tracerProvider, "encryptor"),
		key:    key32,
	}, nil
}
