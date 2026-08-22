package pubsub

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/consumererr"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/mqmetrics"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/panicking"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
)

type (
	pubSubConsumer struct {
		o11y        observability.Observer
		instruments *mqmetrics.Consumer
		consumer    *pubsub.Client
		handlerFunc func(context.Context, []byte) error
		topic       string
	}
)

// buildPubSubConsumer provides a Pub/Sub-backed pubSubConsumer.
func buildPubSubConsumer(
	logger logging.Logger,
	tracerProvider tracing.Provider,
	metricsProvider metrics.Provider,
	pubsubClient *pubsub.Client,
	topic string,
	handlerFunc func(context.Context, []byte) error,
) (messagequeue.Consumer, error) {
	instruments, err := mqmetrics.NewConsumer(metricsProvider, topic)
	if err != nil {
		return nil, err
	}

	return &pubSubConsumer{
		topic:       topic,
		o11y:        observability.NewObserverWithValues(fmt.Sprintf("%s_consumer", topic), logger, tracerProvider, map[string]any{keys.TopicKey: topic}),
		consumer:    pubsubClient,
		handlerFunc: handlerFunc,
		instruments: instruments,
	}, nil
}

// subscriptionNameForTopic resolves the subscription resource name for a topic.
// A fully qualified topic (projects/{project}/topics/{id}) maps to the sibling
// subscription; a short name is qualified with projectID, mirroring how the
// publisher qualifies short topic names.
func subscriptionNameForTopic(projectID, topic string) string {
	if strings.HasPrefix(topic, "projects/") {
		return strings.Replace(topic, "/topics/", "/subscriptions/", 1)
	}
	return fmt.Sprintf("projects/%s/subscriptions/%s", projectID, topic)
}

func (c *pubSubConsumer) Consume(ctx context.Context, errs chan<- error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	subscriptionName := subscriptionNameForTopic(c.consumer.Project(), c.topic)

	sub, err := c.consumer.SubscriptionAdminClient.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{
		Subscription: subscriptionName,
	})
	if err != nil {
		c.o11y.Logger().Error(fmt.Sprintf("getting %s subscription", subscriptionName), err)
		consumererr.Send(ctx, errs, err)

		return
	}

	subscriber := c.consumer.Subscriber(sub.GetName())

	if err = subscriber.Receive(ctx, func(receivedContext context.Context, m *pubsub.Message) {
		msgCtx, op := c.o11y.BeginCustom(receivedContext, "consume_message")
		defer op.End()

		op.Set(keys.LengthKey, len(m.Data))
		op.SpanOnly("message_id", m.ID)
		if m.DeliveryAttempt != nil {
			op.SpanOnly("delivery_attempt", *m.DeliveryAttempt)
		}

		startedAt := time.Now()
		handleErr := panicking.Contain(func() error { return c.handlerFunc(msgCtx, m.Data) })
		c.instruments.Handled(msgCtx, startedAt, handleErr)

		if handleErr != nil {
			op.Acknowledge(handleErr, "handling pubsub message")
			m.Nack()
			consumererr.Send(msgCtx, errs, handleErr)
		} else {
			m.Ack()
		}
	}); err != nil && ctx.Err() == nil {
		// Receive only returns on a non-retryable failure, which means this
		// consumer has stopped consuming. It used to be logged and dropped, so the
		// owner of errs — the caller that asked to be told about consumer failures
		// — went on waiting for messages from a subscription nothing was reading.
		c.instruments.ReceiveFailed(ctx)
		c.o11y.Logger().Error(fmt.Sprintf("receiving %s pub/sub data", c.topic), err)
		consumererr.Send(ctx, errs, err)
	}
}

var _ messagequeue.ConsumerProvider = (*ConsumerProvider)(nil)

// ConsumerProvider is the Google Cloud Pub/Sub messagequeue.ConsumerProvider implementation. It is
// exported, and returned by NewPubSubConsumerProvider, so a caller who has chosen
// Google Cloud Pub/Sub can depend on that choice rather than on the interface every
// broker shares.
type ConsumerProvider struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	consumerCache   map[string]messagequeue.Consumer
	pubsubClient    *pubsub.Client
	consumerCacheMu sync.RWMutex
}

// NewPubSubConsumerProvider returns a ConsumerProvider for a given address.
func NewPubSubConsumerProvider(client *pubsub.Client, opts ...Option) *ConsumerProvider {
	o := newOptions(opts)

	return &ConsumerProvider{
		logger:          logging.EnsureLogger(o.logger),
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
		pubsubClient:    client,
		consumerCache:   map[string]messagequeue.Consumer{},
	}
}

// Close closes the connection topic.
func (p *ConsumerProvider) Close() {
	if err := p.pubsubClient.Close(); err != nil {
		p.logger.Error("closing pubsub connection", err)
	}
}

// NewConsumer returns a pubSubConsumer for a given topic.
func (p *ConsumerProvider) NewConsumer(_ context.Context, topic string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	if topic == "" {
		return nil, messagequeue.ErrEmptyTopicName
	}

	logger := logging.EnsureLogger(p.logger.Clone())

	p.consumerCacheMu.Lock()
	defer p.consumerCacheMu.Unlock()
	if cachedPub, ok := p.consumerCache[topic]; ok {
		return cachedPub, nil
	}

	pub, err := buildPubSubConsumer(logger, p.tracerProvider, p.metricsProvider, p.pubsubClient, topic, handlerFunc)
	if err != nil {
		return nil, err
	}
	p.consumerCache[topic] = pub

	return pub, nil
}
