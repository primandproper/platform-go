package config

import (
	"github.com/primandproper/platform-go/cryptography/encryption"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterEncryptorDecryptor registers an encryption.EncryptorDecryptor with the injector.
func RegisterEncryptorDecryptor(i do.Injector) {
	do.Provide[encryption.EncryptorDecryptor](i, func(i do.Injector) (encryption.EncryptorDecryptor, error) {
		return ProvideEncryptorDecryptor(
			do.MustInvoke[*Config](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[[]byte](i),
		)
	})
}
