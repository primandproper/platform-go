package tracingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"
)

// NewTracerProvider provides a TracerProvider from config.
func NewTracerProvider(ctx context.Context, c *Config, l logging.Logger) (traceProvider tracing.TracerProvider, err error) {
	return c.NewTracerProvider(ctx, l)
}
