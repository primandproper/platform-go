package analyticscfg

import (
	"context"

	"github.com/primandproper/platform-go/v5/analytics"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"
)

// NewEventReporter provides an analytics.EventReporter from a config.
func NewEventReporter(ctx context.Context, cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (analytics.EventReporter, error) {
	return cfg.NewCollector(ctx, logger, tracerProvider, metricsProvider)
}
