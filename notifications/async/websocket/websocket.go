package websocket

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/eventstream"
	eswebsocket "github.com/primandproper/platform-go/eventstream/websocket"
	"github.com/primandproper/platform-go/notifications/async"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"
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
func NewNotifier(cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider) (*Notifier, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	wsCfg := &eswebsocket.Config{
		HeartbeatInterval: cfg.HeartbeatInterval,
		ReadBufferSize:    cfg.ReadBufferSize,
		WriteBufferSize:   cfg.WriteBufferSize,
	}

	return &Notifier{
		o11y:     observability.NewObserver(o11yName, logger, tracerProvider),
		upgrader: eswebsocket.NewUpgrader(logger, tracerProvider, wsCfg),
		manager:  eventstream.NewStreamManager[eventstream.EventStream](tracerProvider, logger),
	}, nil
}

// Publish sends an event to all connected clients on the given channel.
func (n *Notifier) Publish(ctx context.Context, channel string, event *async.Event) error {
	ctx, op := n.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.TopicKey, channel).Set("event.type", event.Type).Set(keys.LengthKey, len(event.Data))

	esEvent := &eventstream.Event{
		Type:    event.Type,
		Payload: event.Data,
	}

	n.manager.BroadcastToGroup(ctx, channel, esEvent)

	return nil
}

// AcceptConnection upgrades the HTTP connection to a WebSocket and registers it
// under the given channel and memberID.
func (n *Notifier) AcceptConnection(w http.ResponseWriter, r *http.Request, channel, memberID string) error {
	ctx, op := n.o11y.Begin(r.Context())
	defer op.End()

	op.Set(keys.TopicKey, channel).Set("member_id", memberID)

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
