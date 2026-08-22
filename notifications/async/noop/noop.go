// Package noop is the async.AsyncNotifier that publishes to nobody. Publish
// accepts every event on every channel and returns nil: no connected client
// receives it, and no hosted broker is called.
//
// It differs from the sse and websocket providers in kind rather than in
// degree. Those deliver to the clients connected to this replica and miss the
// clients connected elsewhere; this one misses all of them, by design and
// identically at any replica count. Reach for it where a service emits events
// no deployment currently consumes, or in tests that exercise the publish path
// without standing up a transport.
//
// notifications/async/config builds it for the "noop" provider name.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/notifications/async"
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
