package config

import (
	"github.com/primandproper/platform-go/capitalism"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterPaymentManager registers a capitalism.PaymentManager with the injector.
func RegisterPaymentManager(i do.Injector) {
	do.Provide[capitalism.PaymentManager](i, func(i do.Injector) (capitalism.PaymentManager, error) {
		return ProvideCapitalismImplementation(
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[*Config](i),
		)
	})
}
