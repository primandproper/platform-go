package pusher

import (
	"context"

	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/notifications/async"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	pushersdk "github.com/pusher/pusher-http-go/v5"
)

const o11yName = "async_notifications_pusher"

var (
	_ async.AsyncNotifier = (*Notifier)(nil)

	ErrNilConfig = errors.New("pusher config is nil")
)

// PusherClient abstracts the Pusher SDK client for testability.
type PusherClient interface {
	Trigger(channel string, eventName string, data any) error
}

// Notifier is a Pusher-backed AsyncNotifier.
type Notifier struct {
	o11y         observability.Observer
	client       PusherClient
	sendCounter  metrics.Int64Counter
	errorCounter metrics.Int64Counter
}

// NewNotifier creates a new Pusher-backed AsyncNotifier.
func NewNotifier(cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (*Notifier, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	client := &pushersdk.Client{
		AppID:   cfg.AppID,
		Key:     cfg.Key,
		Secret:  cfg.Secret,
		Cluster: cfg.Cluster,
		Secure:  cfg.Secure,
	}

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	sendCounter, err := mp.NewInt64Counter(o11yName + "_sends")
	if err != nil {
		return nil, errors.Wrap(err, "creating send counter")
	}

	errorCounter, err := mp.NewInt64Counter(o11yName + "_errors")
	if err != nil {
		return nil, errors.Wrap(err, "creating error counter")
	}

	return &Notifier{
		o11y:         observability.NewObserver(o11yName, logger, tracerProvider),
		client:       client,
		sendCounter:  sendCounter,
		errorCounter: errorCounter,
	}, nil
}

// Publish sends an event to the given Pusher channel.
func (n *Notifier) Publish(ctx context.Context, channel string, event *async.Event) error {
	ctx, op := n.o11y.Begin(ctx)
	defer op.End()

	op.Set("pusher.channel", channel).Set("pusher.event_type", event.Type)

	if err := n.client.Trigger(channel, event.Type, event.Data); err != nil {
		n.errorCounter.Add(ctx, 1)
		return op.Error(err, "publishing to pusher channel")
	}

	n.sendCounter.Add(ctx, 1)
	return nil
}

// Close is a no-op for the Pusher notifier (stateless HTTP API).
func (n *Notifier) Close() error {
	return nil
}
