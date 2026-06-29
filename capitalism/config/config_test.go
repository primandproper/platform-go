package config

import (
	"testing"

	"github.com/primandproper/platform-go/v2/capitalism/stripe"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Enabled:  true,
			Provider: StripeProvider,
			Stripe:   &stripe.Config{APIKey: t.Name()},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("returns nil when not enabled", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Enabled: false,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with invalid config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Enabled:  true,
			Provider: StripeProvider,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestProvideCapitalismImplementation(T *testing.T) {
	T.Parallel()

	T.Run("with stripe provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: StripeProvider,
			Stripe:   &stripe.Config{APIKey: t.Name()},
		}

		pm, err := ProvideCapitalismImplementation(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg)
		must.NoError(t, err)
		test.NotNil(t, pm)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "unknown",
		}

		pm, err := ProvideCapitalismImplementation(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg)
		test.Nil(t, pm)
		test.Error(t, err)
	})
}
