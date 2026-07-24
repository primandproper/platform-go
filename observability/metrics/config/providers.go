package metricscfg

import (
	"context"

	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
)

// NewMetricsProvider provides a metrics.Provider from config.
func NewMetricsProvider(ctx context.Context, logger logging.Logger, c *Config) (metrics.Provider, error) {
	return c.NewMetricsProvider(ctx, logger)
}
