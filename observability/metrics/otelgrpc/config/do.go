package config

import (
	"context"

	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"

	"github.com/samber/do/v2"
)

// RegisterMetricsProvider registers a metrics.Provider with the injector.
func RegisterMetricsProvider(i do.Injector) {
	do.Provide[metrics.Provider](i, func(i do.Injector) (metrics.Provider, error) {
		return ProvideMetricsProvider(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[*Config](i),
		)
	})
}
