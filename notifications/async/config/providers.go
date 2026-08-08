package asynccfg

import (
	"context"

	"github.com/primandproper/platform-go/v10/messagequeue"
	"github.com/primandproper/platform-go/v10/notifications/async"
)

// NewAsyncNotifier provides an AsyncNotifier from a config.
//
// The messagequeue providers are the deps this package cannot build, and they
// are only reached when the config enables fan-out — a deployment without a
// backplane passes nil for both. See fanout for what they are used for.
func NewAsyncNotifier(
	ctx context.Context,
	cfg *Config,
	publisherProvider messagequeue.PublisherProvider,
	consumerProvider messagequeue.ConsumerProvider,
	opts ...Option,
) (async.AsyncNotifier, error) {
	return cfg.NewAsyncNotifier(ctx, publisherProvider, consumerProvider, opts...)
}
