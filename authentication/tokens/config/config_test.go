package tokenscfg

import (
	"encoding/base64"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"
	"github.com/primandproper/platform-go/v6/random"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider:                ProviderJWT,
			Issuer:                  t.Name(),
			Audience:                t.Name(),
			Base64EncodedSigningKey: base64.URLEncoding.EncodeToString(random.MustGenerateRawBytes(ctx, 32)),
		}

		must.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with missing key", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderJWT,
		}

		must.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with invalid provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider:                "not-a-real-provider",
			Issuer:                  t.Name(),
			Audience:                t.Name(),
			Base64EncodedSigningKey: base64.URLEncoding.EncodeToString(random.MustGenerateRawBytes(ctx, 32)),
		}

		must.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with empty provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider:                "",
			Issuer:                  t.Name(),
			Audience:                t.Name(),
			Base64EncodedSigningKey: base64.URLEncoding.EncodeToString(random.MustGenerateRawBytes(ctx, 32)),
		}

		must.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestConfig_NewTokenIssuer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			Provider:                ProviderJWT,
			Issuer:                  t.Name(),
			Audience:                t.Name(),
			Base64EncodedSigningKey: base64.URLEncoding.EncodeToString(random.MustGenerateRawBytes(ctx, 32)),
		}

		actual, err := cfg.NewTokenIssuer(logger, tracingnoop.NewTracerProvider())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with invalid provider", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{
			Provider: "",
		}

		actual, err := cfg.NewTokenIssuer(logger, tracingnoop.NewTracerProvider())
		test.Nil(t, actual)
		test.Error(t, err)
	})

	T.Run("with PASETO provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider:                ProviderPASETO,
			Issuer:                  t.Name(),
			Audience:                t.Name(),
			Base64EncodedSigningKey: base64.URLEncoding.EncodeToString(random.MustGenerateRawBytes(ctx, 32)),
		}

		actual, err := cfg.NewTokenIssuer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)
		test.NotNil(t, actual)
	})

	T.Run("with unknown provider returns an error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider:                "some-unknown-provider",
			Issuer:                  t.Name(),
			Audience:                t.Name(),
			Base64EncodedSigningKey: base64.URLEncoding.EncodeToString(random.MustGenerateRawBytes(ctx, 32)),
		}

		actual, err := cfg.NewTokenIssuer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		test.Error(t, err)
		test.Nil(t, actual)
	})

	T.Run("with empty provider returns an error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider:                "",
			Issuer:                  t.Name(),
			Audience:                t.Name(),
			Base64EncodedSigningKey: base64.URLEncoding.EncodeToString(random.MustGenerateRawBytes(ctx, 32)),
		}

		actual, err := cfg.NewTokenIssuer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		test.Error(t, err)
		test.Nil(t, actual)
	})

	T.Run("with invalid base64 signing key", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:                ProviderJWT,
			Issuer:                  t.Name(),
			Audience:                t.Name(),
			Base64EncodedSigningKey: "not-valid-base64!!!",
		}

		actual, err := cfg.NewTokenIssuer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		test.Error(t, err)
		test.Nil(t, actual)
	})

	T.Run("with wrong signing key length", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider:                ProviderJWT,
			Issuer:                  t.Name(),
			Audience:                t.Name(),
			Base64EncodedSigningKey: base64.URLEncoding.EncodeToString(random.MustGenerateRawBytes(ctx, 16)),
		}

		actual, err := cfg.NewTokenIssuer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		test.Error(t, err)
		test.Nil(t, actual)
	})
}
