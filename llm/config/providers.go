package llmcfg

import (
	"context"

	"github.com/primandproper/platform-go/v4/llm"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"
)

// NewLLMProvider provides an LLM provider from config.
func NewLLMProvider(ctx context.Context, c *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (llm.Provider, error) {
	return c.NewLLMProvider(ctx, logger, tracerProvider, metricsProvider)
}
