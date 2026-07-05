package config

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v4/capitalism"
	"github.com/primandproper/platform-go/v4/capitalism/stripe"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	stripego "github.com/stripe/stripe-go/v75"
)

func TestRegisterPaymentManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, &Config{
			Enabled:  true,
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
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, &Config{
			Enabled:  true,
			Provider: StripeProvider,
			Stripe:   &stripe.Config{WebhookSecret: t.Name()},
		})

		var handler stripe.EventHandler = func(context.Context, *stripego.Event) error { return nil }
		do.ProvideValue(i, handler)

		RegisterPaymentManager(i)

		pm, err := do.Invoke[capitalism.PaymentManager](i)
		must.NoError(t, err)
		test.NotNil(t, pm)
	})
}
