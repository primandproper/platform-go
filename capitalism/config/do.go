package capitalismcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/capitalism"
	"github.com/primandproper/platform-go/v13/capitalism/stripe"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterPaymentManager registers a capitalism.PaymentManager with the injector. A
// stripe.EventHandler may optionally be registered in the container; when present, it is wired into
// the Stripe manager so consumers can act on verified webhook events.
func RegisterPaymentManager(i do.Injector) {
	do.Provide(i, func(i do.Injector) (capitalism.PaymentManager, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		// The event handler is optional; a resolution error means none was registered.
		var stripeEventHandler stripe.EventHandler
		if h, handlerErr := do.Invoke[stripe.EventHandler](i); handlerErr == nil {
			stripeEventHandler = h
		}

		return NewPaymentManager(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			stripeEventHandler,
			WithPillars(pillars),
		)
	})
}

// RegisterUsageReporter registers a capitalism.UsageReporter with the injector.
//
// It is a separate registration from RegisterPaymentManager because the two are
// wanted by different processes: an API server charges, and a worker reports
// usage. A deployment registers whichever of the two it actually runs.
func RegisterUsageReporter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (capitalism.UsageReporter, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewUsageReporter(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}
