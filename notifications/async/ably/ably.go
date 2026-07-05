package ably

import (
	"context"
	"encoding/json"

	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/notifications/async"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"

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
	o11y         observability.Observer
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
		o11y:         observability.NewObserver(o11yName, logger, tracerProvider),
		publisher:    &ablyChannelPublisher{client: client},
		sendCounter:  sendCounter,
		errorCounter: errorCounter,
	}, nil
}

// Publish sends an event to the given Ably channel.
func (n *Notifier) Publish(ctx context.Context, channel string, event *async.Event) error {
	ctx, op := n.o11y.Begin(ctx)
	defer op.End()

	op.Set("channel", channel).Set("event.type", event.Type)

	// event.Data is a json.RawMessage ([]byte). Passing it to ably-go directly
	// makes it base64-encode the payload (encoding "base64"), so subscribers
	// receive an opaque blob instead of the raw JSON the other backends deliver.
	// Decode it into a Go value so ably-go transmits it as a JSON object/array.
	var data any
	if len(event.Data) > 0 {
		if err := json.Unmarshal(event.Data, &data); err != nil {
			n.errorCounter.Add(ctx, 1)
			return op.Error(err, "decoding event data")
		}
	}

	if err := n.publisher.Publish(ctx, channel, event.Type, data); err != nil {
		n.errorCounter.Add(ctx, 1)
		return op.Error(err, "publishing to ably channel")
	}

	n.sendCounter.Add(ctx, 1)
	return nil
}

// Close is a no-op for the Ably notifier (REST client, no persistent connection).
func (n *Notifier) Close() error {
	return nil
}
