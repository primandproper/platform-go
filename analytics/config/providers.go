package analyticscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/analytics"
)

// NewEventReporter provides an analytics.EventReporter from a config.
func NewEventReporter(ctx context.Context, cfg *Config, opts ...Option) (analytics.EventReporter, error) {
	return cfg.NewCollector(ctx, opts...)
}
