package sse

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v14/notifications/async"

	"github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/eventstream"
	essse "github.com/primandproper/primitives-go/eventstream/sse"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/keys"
)

const o11yName = "async_notifications_sse"

var (
	_ async.AsyncNotifier      = (*Notifier)(nil)
	_ async.ConnectionAcceptor = (*Notifier)(nil)

	// ErrNilConfig indicates a nil *Config.
	//
	// SSE has nothing to configure — the Config is empty, and everything this
	// notifier needs arrives as options — so a nil one could be accepted without
	// consequence. It is refused anyway, because the websocket sibling refuses
	// one and a caller wiring both from the same config tree should not have to
	// remember which of the two tolerates a missing block. A Config that later
	// grows a field would turn the tolerated nil into a silent default.
	ErrNilConfig = errors.New("sse async notifier config is nil")
)

// Notifier is an SSE-backed AsyncNotifier that manages direct client connections.
// Note that AcceptConnection blocks the calling goroutine for the lifetime of the
// client connection, as SSE uses the HTTP response writer directly.
type Notifier struct {
	o11y     observability.Observer
	upgrader *essse.Upgrader
	manager  *eventstream.StreamManager[eventstream.EventStream]
}

// NewNotifier creates a new SSE-backed AsyncNotifier.
func NewNotifier(cfg *Config, opts ...Option) (*Notifier, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	o := newOptions(opts)

	return &Notifier{
		o11y:     observability.NewObserver(o11yName, o.logger, o.tracerProvider),
		upgrader: essse.NewUpgrader(essse.WithLogger(o.logger), essse.WithTracerProvider(o.tracerProvider)),
		manager:  eventstream.NewStreamManager[eventstream.EventStream](eventstream.WithTracerProvider(o.tracerProvider), eventstream.WithLogger(o.logger)),
	}, nil
}

// Publish sends an event to all connected clients on the given channel.
//
// One client's failure does not stop the others: every connection on the channel
// is attempted, and the joined per-client failures come back from here, so a
// non-nil error means the event reached a subset of the channel.
func (n *Notifier) Publish(ctx context.Context, channel string, event *async.Event) error {
	ctx, op := n.o11y.Begin(ctx,
		observability.WithValue(keys.ChannelKey, channel),
		observability.WithValue(keys.EventTypeKey, event.Type),
		observability.WithValue(keys.LengthKey, len(event.Data)),
	)
	defer op.End()

	esEvent := &eventstream.Event{
		Type:    event.Type,
		Payload: event.Data,
	}

	if err := n.manager.BroadcastToGroup(ctx, channel, esEvent); err != nil {
		return op.Error(err, "broadcasting event to channel")
	}

	return nil
}

// AcceptConnection upgrades the HTTP connection to an SSE stream and registers it
// under the given channel and memberID. This method blocks the calling goroutine
// for the lifetime of the client connection.
func (n *Notifier) AcceptConnection(w http.ResponseWriter, r *http.Request, channel, memberID string) error {
	ctx, op := n.o11y.Begin(r.Context())
	defer op.End()

	op.Set(keys.ChannelKey, channel).Set(keys.MemberIDKey, memberID)

	stream, err := n.upgrader.UpgradeToEventStream(w, r)
	if err != nil {
		return op.Error(err, "upgrading SSE connection")
	}

	n.manager.Add(ctx, channel, memberID, stream)

	defer func(removeCtx context.Context) {
		n.manager.Remove(removeCtx, channel, memberID)
	}(ctx)

	// Block until the client disconnects.
	<-stream.Done()

	return nil
}

// Close releases resources held by the notifier.
func (n *Notifier) Close() error {
	return nil
}
