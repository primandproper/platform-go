package websocket

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v14/notifications/async"

	"github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/eventstream"
	eswebsocket "github.com/primandproper/primitives-go/eventstream/websocket"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/keys"
)

const o11yName = "async_notifications_websocket"

var (
	_ async.AsyncNotifier      = (*Notifier)(nil)
	_ async.ConnectionAcceptor = (*Notifier)(nil)

	ErrNilConfig = errors.New("websocket async notifier config is nil")
)

// Notifier is a WebSocket-backed AsyncNotifier that manages direct client connections.
type Notifier struct {
	o11y     observability.Observer
	upgrader *eswebsocket.Upgrader
	manager  *eventstream.StreamManager[eventstream.EventStream]
}

// NewNotifier creates a new WebSocket-backed AsyncNotifier.
func NewNotifier(cfg *Config, opts ...Option) (*Notifier, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	o := newOptions(opts)

	wsCfg := &eswebsocket.Config{
		HeartbeatInterval: cfg.HeartbeatInterval,
		ReadBufferSize:    cfg.ReadBufferSize,
		WriteBufferSize:   cfg.WriteBufferSize,
	}

	return &Notifier{
		o11y:     observability.NewObserver(o11yName, o.logger, o.tracerProvider),
		upgrader: eswebsocket.NewUpgrader(wsCfg, eswebsocket.WithLogger(o.logger), eswebsocket.WithTracerProvider(o.tracerProvider)),
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

// AcceptConnection upgrades the HTTP connection to a WebSocket and registers it
// under the given channel and memberID.
func (n *Notifier) AcceptConnection(w http.ResponseWriter, r *http.Request, channel, memberID string) error {
	ctx, op := n.o11y.Begin(r.Context())
	defer op.End()

	op.Set(keys.ChannelKey, channel).Set(keys.MemberIDKey, memberID)

	stream, err := n.upgrader.UpgradeToEventStream(w, r)
	if err != nil {
		return op.Error(err, "upgrading websocket connection")
	}

	n.manager.Add(ctx, channel, memberID, stream)

	go func(removeCtx context.Context) {
		<-stream.Done()
		n.manager.Remove(removeCtx, channel, memberID)
	}(ctx)

	return nil
}

// Close releases resources held by the notifier.
func (n *Notifier) Close() error {
	return nil
}
