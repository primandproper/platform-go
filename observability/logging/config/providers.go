package loggingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v5/observability/logging"
)

// NewLogger provides a Logger from config.
func NewLogger(ctx context.Context, cfg *Config) (logging.Logger, error) {
	return cfg.NewLogger(ctx)
}
