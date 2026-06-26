package profilingcfg

import (
	"context"

	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/profiling"
)

// ProvideProfilingProviderWire provides a profiling provider from config.
func ProvideProfilingProviderWire(ctx context.Context, logger logging.Logger, c *Config) (profiling.Provider, error) {
	return c.ProvideProfilingProvider(ctx, logger)
}
