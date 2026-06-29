package llmcfg

import (
	"context"

	"github.com/primandproper/platform-go/v2/llm"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"
)

// ProvideLLMProvider provides an LLM provider from config.
func ProvideLLMProvider(c *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (llm.Provider, error) {
	return c.ProvideLLMProvider(context.Background(), logger, tracerProvider, metricsProvider)
}
