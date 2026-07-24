package config

import (
	"github.com/primandproper/platform-go/v6/capitalism"
	"github.com/primandproper/platform-go/v6/capitalism/stripe"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterPaymentManager registers a capitalism.PaymentManager with the injector. A
// stripe.EventHandler may optionally be registered in the container; when present, it is wired into
// the Stripe manager so consumers can act on verified webhook events.
func RegisterPaymentManager(i do.Injector) {
	do.Provide[capitalism.PaymentManager](i, func(i do.Injector) (capitalism.PaymentManager, error) {
		// The event handler is optional; a resolution error means none was registered.
		var stripeEventHandler stripe.EventHandler
		if h, err := do.Invoke[stripe.EventHandler](i); err == nil {
			stripeEventHandler = h
		}

		return NewCapitalismImplementation(
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[*Config](i),
			stripeEventHandler,
		)
	})
}
