package llmcfg

import (
	"context"

	"github.com/primandproper/platform-go/v4/llm"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"
)

// ProvideLLMProvider provides an LLM provider from config.
func ProvideLLMProvider(ctx context.Context, c *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (llm.Provider, error) {
	return c.ProvideLLMProvider(ctx, logger, tracerProvider, metricsProvider)
}
