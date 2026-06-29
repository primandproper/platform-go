package loggingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v2/observability/logging"
)

// ProvideLogger provides a Logger from config.
func ProvideLogger(ctx context.Context, cfg *Config) (logging.Logger, error) {
	return cfg.ProvideLogger(ctx)
}
