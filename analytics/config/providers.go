package analyticscfg

import (
	"context"

	"github.com/primandproper/platform-go/v3/analytics"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	"github.com/primandproper/platform-go/v3/observability/tracing"
)

// ProvideEventReporter provides an analytics.EventReporter from a config.
func ProvideEventReporter(ctx context.Context, cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (analytics.EventReporter, error) {
	return cfg.ProvideCollector(ctx, logger, tracerProvider, metricsProvider)
}
