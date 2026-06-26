package secretscfg

import (
	"context"

	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"
	"github.com/primandproper/platform-go/secrets"

	"github.com/samber/do/v2"
)

// RegisterSecretSource registers a secrets.SecretSource with the injector.
func RegisterSecretSource(i do.Injector) {
	do.Provide[secrets.SecretSource](i, func(i do.Injector) (secrets.SecretSource, error) {
		return ProvideSecretSourceFromConfig(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})
}
