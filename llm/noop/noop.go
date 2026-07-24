package noop

import (
	"context"

	"github.com/primandproper/platform-go/v6/llm"
)

var _ llm.Provider = (*Provider)(nil)

// Provider is a no-op Provider.
type Provider struct{}

// NewProvider returns a no-op Provider.
func NewProvider() llm.Provider {
	return &Provider{}
}

// Completion is a no-op that returns an empty result.
func (*Provider) Completion(context.Context, llm.CompletionParams) (*llm.CompletionResult, error) {
	return &llm.CompletionResult{}, nil
}
