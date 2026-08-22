package capitalismcfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/capitalism"
	"github.com/primandproper/platform-go/v13/capitalism/stripe"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterPaymentManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, &Config{
			Provider: StripeProvider,
			Stripe:   &stripe.Config{WebhookSecret: t.Name()},
		})

		RegisterPaymentManager(i)

		pm, err := do.Invoke[capitalism.PaymentManager](i)
		must.NoError(t, err)
		test.NotNil(t, pm)
	})

	T.Run("wires a registered stripe event handler", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, &Config{
			Provider: StripeProvider,
			Stripe:   &stripe.Config{WebhookSecret: t.Name()},
		})

		var handler stripe.EventHandler = func(context.Context, *stripe.Event) error { return nil }
		do.ProvideValue(i, handler)

		RegisterPaymentManager(i)

		pm, err := do.Invoke[capitalism.PaymentManager](i)
		must.NoError(t, err)
		test.NotNil(t, pm)
	})
}

func TestRegisterUsageReporter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, &Config{
			Provider: StripeProvider,
			Stripe:   &stripe.Config{APIKey: "sk_test_123", WebhookSecret: t.Name()},
		})

		RegisterUsageReporter(i)

		reporter, err := do.Invoke[capitalism.UsageReporter](i)
		must.NoError(t, err)
		test.NotNil(t, reporter)
	})

	T.Run("is registered independently of the payment manager", func(t *testing.T) {
		t.Parallel()

		// The two are wanted by different processes — an API server charges, a
		// worker reports usage — so a deployment registers whichever it runs.
		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, &Config{Provider: NoopProvider})

		RegisterUsageReporter(i)

		reporter, err := do.Invoke[capitalism.UsageReporter](i)
		must.NoError(t, err)
		test.NotNil(t, reporter)

		_, err = do.Invoke[capitalism.PaymentManager](i)
		test.Error(t, err)
	})
}
