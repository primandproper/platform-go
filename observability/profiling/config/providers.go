package profilingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/profiling"
)

// NewProfilingProvider provides a profiling provider from config.
func NewProfilingProvider(ctx context.Context, logger logging.Logger, c *Config) (profiling.Provider, error) {
	return c.NewProfilingProvider(ctx, logger)
}
