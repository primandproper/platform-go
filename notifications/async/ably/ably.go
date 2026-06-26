package ably

import (
	"context"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/notifications/async"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

	ablyrest "github.com/ably/ably-go/ably"
)

const o11yName = "async_notifications_ably"

var (
	_ async.AsyncNotifier = (*Notifier)(nil)

	ErrNilConfig = errors.New("ably config is nil")
)

// ChannelPublisher abstracts Ably channel publishing for testability.
type ChannelPublisher interface {
	Publish(ctx context.Context, channel, name string, data any) error
}

// ablyChannelPublisher is the real implementation wrapping the Ably REST client.
type ablyChannelPublisher struct {
	client *ablyrest.REST
}

func (a *ablyChannelPublisher) Publish(ctx context.Context, channel, name string, data any) error {
	return a.client.Channels.Get(channel).Publish(ctx, name, data)
}

// Notifier is an Ably-backed AsyncNotifier.
type Notifier struct {
	logger       logging.Logger
	tracer       tracing.Tracer
	publisher    ChannelPublisher
	sendCounter  metrics.Int64Counter
	errorCounter metrics.Int64Counter
}

// NewNotifier creates a new Ably-backed AsyncNotifier.
func NewNotifier(cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (*Notifier, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	client, err := ablyrest.NewREST(ablyrest.WithKey(cfg.APIKey))
	if err != nil {
		return nil, errors.Wrap(err, "creating ably client")
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
		publisher:    &ablyChannelPublisher{client: client},
		sendCounter:  sendCounter,
		errorCounter: errorCounter,
	}, nil
}

// Publish sends an event to the given Ably channel.
func (n *Notifier) Publish(ctx context.Context, channel string, event *async.Event) error {
	_, span := n.tracer.StartSpan(ctx)
	defer span.End()

	if err := n.publisher.Publish(ctx, channel, event.Type, event.Data); err != nil {
		n.errorCounter.Add(ctx, 1)
		return observability.PrepareAndLogError(err, n.logger, span, "publishing to ably channel")
	}

	n.sendCounter.Add(ctx, 1)
	return nil
}

// Close is a no-op for the Ably notifier (REST client, no persistent connection).
func (n *Notifier) Close() error {
	return nil
}
