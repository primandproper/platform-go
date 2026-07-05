package llmcfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v4/llm"
	"github.com/primandproper/platform-go/v4/llm/anthropic"
	llmnoop "github.com/primandproper/platform-go/v4/llm/noop"
	"github.com/primandproper/platform-go/v4/llm/openai"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderOpenAI is the OpenAI provider.
	ProviderOpenAI = "openai"
	// ProviderAnthropic is the Anthropic provider.
	ProviderAnthropic = "anthropic"
)

// Config is the configuration for the LLM provider.
type Config struct {
	OpenAI    *openai.Config    `env:",init"    envPrefix:"OPENAI_"    json:"openai"`
	Anthropic *anthropic.Config `env:",init"    envPrefix:"ANTHROPIC_" json:"anthropic"`
	Provider  string            `env:"PROVIDER" json:"provider"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Provider, validation.In(ProviderOpenAI, ProviderAnthropic, "")),
		validation.Field(&c.OpenAI, validation.When(c.Provider == ProviderOpenAI, validation.Required)),
		validation.Field(&c.Anthropic, validation.When(c.Provider == ProviderAnthropic, validation.Required)),
	)
}

// ProvideLLMProvider provides an LLM provider based on config.
func (c *Config) ProvideLLMProvider(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (llm.Provider, error) {
	switch strings.TrimSpace(strings.ToLower(c.Provider)) {
	case ProviderOpenAI:
		return openai.NewProvider(c.OpenAI, logger, tracerProvider, metricsProvider)
	case ProviderAnthropic:
		return anthropic.NewProvider(c.Anthropic, logger, tracerProvider, metricsProvider)
	default:
		return llmnoop.NewProvider(), nil
	}
}
