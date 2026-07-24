package noop

import (
	"context"

	"github.com/primandproper/platform-go/v6/notifications/async"
)

var _ async.AsyncNotifier = (*asyncNotifier)(nil)

// asyncNotifier is a no-op implementation of AsyncNotifier.
type asyncNotifier struct{}

// NewAsyncNotifier returns a new no-op AsyncNotifier.
func NewAsyncNotifier() (async.AsyncNotifier, error) {
	return &asyncNotifier{}, nil
}

// Publish is a no-op.
func (*asyncNotifier) Publish(context.Context, string, *async.Event) error {
	return nil
}

// Close is a no-op.
func (n *asyncNotifier) Close() error {
	return nil
}
