// Package noop is the profiling.Provider for a deployment that ships no
// profiles. Start launches no Pyroscope agent and registers no pprof handlers,
// so nothing is sampled, no HTTP surface is added to the process, and the CPU
// and allocation cost of continuous profiling is genuinely absent rather than
// merely unexported.
//
// Shutdown has nothing to drain, which is the one way it improves on a real
// provider: it cannot lose the last profile at exit. Profiling is the pillar
// most deployments legitimately do not run, and this is how they say so —
// observability/profiling/config builds it for the "noop" provider name or the
// empty string.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/observability/profiling"
)

var _ profiling.Provider = (*Provider)(nil)

// Provider is a no-op profiling Provider.
type Provider struct{}

// NewProvider returns a no-op Provider.
func NewProvider() *Provider {
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
