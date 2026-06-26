package config

import (
	"testing"

	"github.com/primandproper/platform-go/capitalism"
	"github.com/primandproper/platform-go/capitalism/stripe"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterPaymentManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, &Config{
			Provider: StripeProvider,
			Stripe:   &stripe.Config{APIKey: t.Name()},
		})

		RegisterPaymentManager(i)

		pm, err := do.Invoke[capitalism.PaymentManager](i)
		must.NoError(t, err)
		test.NotNil(t, pm)
	})
}
