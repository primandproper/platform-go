package asynccfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/notifications/async"

	"github.com/primandproper/primitives-go/v2/errors"
)

// NewAsyncNotifier provides an AsyncNotifier from a config.
func NewAsyncNotifier(ctx context.Context, cfg *Config, opts ...Option) (async.AsyncNotifier, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	return cfg.NewAsyncNotifier(ctx, opts...)
}
