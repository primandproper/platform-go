package metricscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/samber/do/v2"
)

// RegisterMetricsProvider registers a metrics.Provider with the injector.
//
// The logger is looked up optionally rather than through do.MustInvoke: a
// container that registers no logger still gets a metrics provider, which just
// says nothing about how it was set up.
func RegisterMetricsProvider(i do.Injector) {
	do.Provide(i, func(i do.Injector) (metrics.Provider, error) {
		logger, err := injection.InvokeOptional[logging.Logger](i)
		if err != nil {
			return nil, err
		}

		return NewMetricsProvider(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithLogger(logger),
		)
	})
}
