package config

import (
	"testing"

	"github.com/primandproper/platform-go/v4/cryptography/encryption"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterEncryptorDecryptor(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, &Config{Provider: ProviderAES})
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, encryption.MasterKey(testKey))

		RegisterEncryptorDecryptor(i)

		encDec, err := do.Invoke[encryption.EncryptorDecryptor](i)
		must.NoError(t, err)
		test.NotNil(t, encDec)
	})
}
