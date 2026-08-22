package metricscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/observability/metrics"
)

// NewMetricsProvider provides a metrics.Provider from a config.
func NewMetricsProvider(ctx context.Context, c *Config, opts ...Option) (metrics.Provider, error) {
	return c.NewMetricsProvider(ctx, opts...)
}
