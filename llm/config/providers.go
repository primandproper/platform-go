package llmcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/llm"
)

// NewLLMProvider provides an LLM provider from config.
func NewLLMProvider(ctx context.Context, c *Config, opts ...Option) (llm.Provider, error) {
	return c.NewLLMProvider(ctx, opts...)
}
