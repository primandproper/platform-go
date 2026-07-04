package config

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"

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

	T.Run("empty provider errors", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}
		test.Error(t, cfg.ValidateWithContext(ctx))
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

	T.Run("empty provider errors", func(t *testing.T) {
		t.Parallel()

		encDec, err := ProvideEncryptorDecryptor(&Config{}, tracerProvider, logger, key)
		test.Error(t, err)
		test.Nil(t, encDec)
	})

	T.Run("unknown provider errors", func(t *testing.T) {
		t.Parallel()

		encDec, err := ProvideEncryptorDecryptor(&Config{Provider: "invalid"}, tracerProvider, logger, key)
		test.Error(t, err)
		test.Nil(t, encDec)
	})

	T.Run("nil config errors", func(t *testing.T) {
		t.Parallel()

		encDec, err := ProvideEncryptorDecryptor(nil, tracerProvider, logger, key)
		test.Error(t, err)
		test.Nil(t, encDec)
	})
}
