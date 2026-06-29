package tracingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/tracing"
)

// ProvideTracerProvider provides a TracerProvider from config.
func ProvideTracerProvider(ctx context.Context, c *Config, l logging.Logger) (traceProvider tracing.TracerProvider, err error) {
	return c.ProvideTracerProvider(ctx, l)
}
