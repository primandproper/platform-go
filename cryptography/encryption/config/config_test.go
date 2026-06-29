package config

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/shoenig/test"
)

const testKey = "blahblahblahblahblahblahblahblah"

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("aes provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{Provider: ProviderAES}
		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("salsa20 provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{Provider: ProviderSalsa20}
		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("empty provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}
		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{Provider: "invalid"}
		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestProvideEncryptorDecryptor(T *testing.T) {
	T.Parallel()

	tracerProvider := tracingnoop.NewTracerProvider()
	logger := loggingnoop.NewLogger()
	key := []byte(testKey)

	T.Run("aes provider", func(t *testing.T) {
		t.Parallel()

		encDec, err := ProvideEncryptorDecryptor(&Config{Provider: ProviderAES}, tracerProvider, logger, key)
		test.NoError(t, err)
		test.NotNil(t, encDec)
	})

	T.Run("salsa20 provider", func(t *testing.T) {
		t.Parallel()

		encDec, err := ProvideEncryptorDecryptor(&Config{Provider: ProviderSalsa20}, tracerProvider, logger, key)
		test.NoError(t, err)
		test.NotNil(t, encDec)
	})

	T.Run("empty provider defaults to salsa20", func(t *testing.T) {
		t.Parallel()

		encDec, err := ProvideEncryptorDecryptor(&Config{}, tracerProvider, logger, key)
		test.NoError(t, err)
		test.NotNil(t, encDec)
	})

	T.Run("invalid provider defaults to salsa20", func(t *testing.T) {
		t.Parallel()

		encDec, err := ProvideEncryptorDecryptor(&Config{Provider: "invalid"}, tracerProvider, logger, key)
		test.NoError(t, err)
		test.NotNil(t, encDec)
	})
}
