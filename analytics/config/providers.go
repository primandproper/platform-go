package analyticscfg

import (
	"context"

	"github.com/primandproper/platform-go/analytics"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"
)

// ProvideEventReporter provides an analytics.EventReporter from a config.
func ProvideEventReporter(ctx context.Context, cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (analytics.EventReporter, error) {
	return cfg.ProvideCollector(ctx, logger, tracerProvider, metricsProvider)
}
