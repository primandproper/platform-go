package noop

import (
	"context"

	"github.com/primandproper/platform-go/v4/observability/profiling"
)

var _ profiling.Provider = (*Provider)(nil)

// Provider is a no-op profiling Provider.
type Provider struct{}

// NewProvider returns a no-op Provider.
func NewProvider() profiling.Provider {
	return &Provider{}
}

// Start is a no-op.
func (*Provider) Start(context.Context) error {
	return nil
}

// Shutdown is a no-op.
func (*Provider) Shutdown(context.Context) error {
	return nil
}
