package pubsub

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/primandproper/platform-go/v6/messagequeue"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
)

type (
	pubSubConsumer struct {
		o11y            observability.Observer
		consumedCounter metrics.Int64Counter
		consumer        *pubsub.Client
		handlerFunc     func(context.Context, []byte) error
		topic           string
	}
)

// buildPubSubConsumer provides a Pub/Sub-backed pubSubConsumer.
func buildPubSubConsumer(
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
	pubsubClient *pubsub.Client,
	topic string,
	handlerFunc func(context.Context, []byte) error,
) (messagequeue.Consumer, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	consumedCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_consumed", topic))
	if err != nil {
		return nil, fmt.Errorf("creating consumed counter: %w", err)
	}

	return &pubSubConsumer{
		topic:           topic,
		o11y:            observability.NewObserver(fmt.Sprintf("%s_consumer", topic), logger, tracerProvider),
		consumer:        pubsubClient,
		handlerFunc:     handlerFunc,
		consumedCounter: consumedCounter,
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

func (c *pubSubConsumer) Consume(ctx context.Context, stopChan chan bool, errors chan error) {
	if stopChan == nil {
		stopChan = make(chan bool, 1)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	subscriptionName := subscriptionNameForTopic(c.consumer.Project(), c.topic)

	sub, err := c.consumer.SubscriptionAdminClient.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{
		Subscription: subscriptionName,
	})
	if err != nil {
		c.o11y.Logger().Error(fmt.Sprintf("getting %s subscription", subscriptionName), err)
		if errors != nil {
			select {
			case errors <- err:
			case <-ctx.Done():
			}
		}
		return
	}

	subscriber := c.consumer.Subscriber(sub.GetName())

	// Also select on ctx.Done so this watcher exits on the normal (external ctx
	// cancellation) shutdown path instead of blocking forever on <-stopChan.
	go func() {
		select {
		case <-stopChan:
			cancel()
		case <-ctx.Done():
		}
	}()

	if err = subscriber.Receive(ctx, func(receivedContext context.Context, m *pubsub.Message) {
		msgCtx, op := c.o11y.BeginCustom(receivedContext, "consume_message")
		defer op.End()

		op.Set(keys.TopicKey, c.topic).Set(keys.LengthKey, len(m.Data))
		op.SpanOnly("message_id", m.ID)
		if m.DeliveryAttempt != nil {
			op.SpanOnly("delivery_attempt", *m.DeliveryAttempt)
		}

		c.consumedCounter.Add(msgCtx, 1)
		if handleErr := c.handlerFunc(msgCtx, m.Data); handleErr != nil {
			op.Acknowledge(handleErr, "handling pubsub message")
			m.Nack()
			if errors != nil {
				select {
				case errors <- handleErr:
				case <-msgCtx.Done():
				}
			}
		} else {
			m.Ack()
		}
	}); err != nil && ctx.Err() == nil {
		c.o11y.Logger().Error(fmt.Sprintf("receiving %s pub/sub data", c.topic), err)
	}
}

type pubsubConsumerProvider struct {
	logger          logging.Logger
	tracerProvider  tracing.TracerProvider
	metricsProvider metrics.Provider
	consumerCache   map[string]messagequeue.Consumer
	pubsubClient    *pubsub.Client
	consumerCacheMu sync.RWMutex
}

// NewPubSubConsumerProvider returns a ConsumerProvider for a given address.
func NewPubSubConsumerProvider(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, client *pubsub.Client) messagequeue.ConsumerProvider {
	return &pubsubConsumerProvider{
		logger:          logging.EnsureLogger(logger),
		tracerProvider:  tracerProvider,
		metricsProvider: metricsProvider,
		pubsubClient:    client,
		consumerCache:   map[string]messagequeue.Consumer{},
	}
}

// Close closes the connection topic.
func (p *pubsubConsumerProvider) Close() {
	if err := p.pubsubClient.Close(); err != nil {
		p.logger.Error("closing pubsub connection", err)
	}
}

// NewConsumer returns a pubSubConsumer for a given topic.
func (p *pubsubConsumerProvider) NewConsumer(_ context.Context, topic string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
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
