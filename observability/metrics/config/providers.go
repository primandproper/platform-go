package metricscfg

import (
	"context"

	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
)

// ProvideMetricsProvider provides a metrics.Provider from config.
func ProvideMetricsProvider(ctx context.Context, logger logging.Logger, c *Config) (metrics.Provider, error) {
	return c.ProvideMetricsProvider(ctx, logger)
}
