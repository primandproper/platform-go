package pusher

import (
	"context"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/notifications/async"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

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
	logger       logging.Logger
	tracer       tracing.Tracer
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
		logger:       logging.NewNamedLogger(logger, o11yName),
		tracer:       tracing.NewNamedTracer(tracerProvider, o11yName),
		client:       client,
		sendCounter:  sendCounter,
		errorCounter: errorCounter,
	}, nil
}

// Publish sends an event to the given Pusher channel.
func (n *Notifier) Publish(ctx context.Context, channel string, event *async.Event) error {
	_, span := n.tracer.StartSpan(ctx)
	defer span.End()

	if err := n.client.Trigger(channel, event.Type, event.Data); err != nil {
		n.errorCounter.Add(ctx, 1)
		return observability.PrepareAndLogError(err, n.logger, span, "publishing to pusher channel")
	}

	n.sendCounter.Add(ctx, 1)
	return nil
}

// Close is a no-op for the Pusher notifier (stateless HTTP API).
func (n *Notifier) Close() error {
	return nil
}
