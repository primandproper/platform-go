package llmcfg

import (
	"context"

	"github.com/primandproper/platform-go/llm"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"
)

// ProvideLLMProvider provides an LLM provider from config.
func ProvideLLMProvider(c *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (llm.Provider, error) {
	return c.ProvideLLMProvider(context.Background(), logger, tracerProvider, metricsProvider)
}
