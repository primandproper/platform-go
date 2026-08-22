package capitalismcfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/capitalism/stripe"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: StripeProvider,
			Stripe:   &stripe.Config{WebhookSecret: t.Name()},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("the noop provider is valid on its own", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{Provider: NoopProvider}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("an unset provider is invalid", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}

		// Turning payments off has to be asked for by name; an unset provider is a
		// mistake rather than a way to say "no payments".
		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with invalid config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: StripeProvider,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestNewPaymentManager(T *testing.T) {
	T.Parallel()

	T.Run("with stripe provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: StripeProvider,
			Stripe:   &stripe.Config{WebhookSecret: t.Name()},
		}

		pm, err := NewPaymentManager(t.Context(), cfg, nil)
		must.NoError(t, err)
		test.NotNil(t, pm)
	})

	T.Run("the noop provider returns the noop manager", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: NoopProvider}

		pm, err := NewPaymentManager(t.Context(), cfg, nil)
		must.NoError(t, err)
		test.NotNil(t, pm)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "unknown",
		}

		pm, err := NewPaymentManager(t.Context(), cfg, nil)
		test.Nil(t, pm)
		test.Error(t, err)
	})
}

func TestNewUsageReporter(T *testing.T) {
	T.Parallel()

	T.Run("with stripe provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: StripeProvider,
			Stripe:   &stripe.Config{APIKey: "sk_test_123", WebhookSecret: t.Name()},
		}

		reporter, err := NewUsageReporter(t.Context(), cfg)
		must.NoError(t, err)
		test.NotNil(t, reporter)
	})

	T.Run("requires an API key for stripe", func(t *testing.T) {
		t.Parallel()

		// There is no inbound path for usage reporting, so a reporter without a
		// key could do nothing at all.
		cfg := &Config{
			Provider: StripeProvider,
			Stripe:   &stripe.Config{WebhookSecret: t.Name()},
		}

		_, err := NewUsageReporter(t.Context(), cfg)
		test.Error(t, err)
	})

	T.Run("disabled returns noop", func(t *testing.T) {
		t.Parallel()

		// "Meter everything, bill nothing" is a supported deployment rather than
		// an error, which is why this yields the noop instead of refusing.
		reporter, err := NewUsageReporter(
			t.Context(),
			&Config{Provider: NoopProvider},
		)
		must.NoError(t, err)
		test.NotNil(t, reporter)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "unknown"}

		reporter, err := NewUsageReporter(t.Context(), cfg)
		test.Nil(t, reporter)
		test.Error(t, err)
	})
}
