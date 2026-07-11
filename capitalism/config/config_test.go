package config

import (
	"testing"

	"github.com/primandproper/platform-go/v4/capitalism/stripe"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

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
			Stripe:   &stripe.Config{WebhookSecret: t.Name()},
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

func TestNewCapitalismImplementation(T *testing.T) {
	T.Parallel()

	T.Run("with stripe provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  true,
			Provider: StripeProvider,
			Stripe:   &stripe.Config{WebhookSecret: t.Name()},
		}

		pm, err := NewCapitalismImplementation(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, nil)
		must.NoError(t, err)
		test.NotNil(t, pm)
	})

	T.Run("disabled returns noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled: false,
		}

		pm, err := NewCapitalismImplementation(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, nil)
		must.NoError(t, err)
		test.NotNil(t, pm)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Enabled:  true,
			Provider: "unknown",
		}

		pm, err := NewCapitalismImplementation(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, nil)
		test.Nil(t, pm)
		test.Error(t, err)
	})
}
