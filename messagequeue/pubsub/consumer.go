package pubsub

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/primandproper/platform-go/messagequeue"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

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
) messagequeue.Consumer {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	consumedCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_consumed", topic))
	if err != nil {
		panic(fmt.Sprintf("creating consumed counter: %v", err))
	}

	return &pubSubConsumer{
		topic:           topic,
		o11y:            observability.NewObserver(fmt.Sprintf("%s_consumer", topic), logger, tracerProvider),
		consumer:        pubsubClient,
		handlerFunc:     handlerFunc,
		consumedCounter: consumedCounter,
	}
}

func subscriptionNameForTopic(topic string) string {
	return strings.Replace(topic, "/topics/", "/subscriptions/", 1)
}

func (c *pubSubConsumer) Consume(ctx context.Context, stopChan chan bool, errors chan error) {
	if stopChan == nil {
		stopChan = make(chan bool, 1)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	subscriptionName := subscriptionNameForTopic(c.topic)

	sub, err := c.consumer.SubscriptionAdminClient.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{
		Subscription: subscriptionName,
	})
	if err != nil {
		c.o11y.Logger().Error(fmt.Sprintf("getting %s subscription", subscriptionName), err)
		errors <- err
		return
	}

	subscriber := c.consumer.Subscriber(sub.GetName())

	go func() {
		<-stopChan
		cancel()
	}()

	if err = subscriber.Receive(ctx, func(receivedContext context.Context, m *pubsub.Message) {
		msgCtx, op := c.o11y.BeginCustom(receivedContext, "consume_message")
		defer op.End()

		op.Set(keys.TopicKey, c.topic)

		c.consumedCounter.Add(msgCtx, 1)
		if handleErr := c.handlerFunc(msgCtx, m.Data); handleErr != nil {
			op.Acknowledge(handleErr, "handling pubsub message")
			errors <- handleErr
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

// ProvidePubSubConsumerProvider returns a ConsumerProvider for a given address.
func ProvidePubSubConsumerProvider(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, client *pubsub.Client) messagequeue.ConsumerProvider {
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

// ProvideConsumer returns a pubSubConsumer for a given topic.
func (p *pubsubConsumerProvider) ProvideConsumer(_ context.Context, topic string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	if topic == "" {
		return nil, messagequeue.ErrEmptyTopicName
	}

	logger := logging.EnsureLogger(p.logger.Clone())

	p.consumerCacheMu.Lock()
	defer p.consumerCacheMu.Unlock()
	if cachedPub, ok := p.consumerCache[topic]; ok {
		return cachedPub, nil
	}

	pub := buildPubSubConsumer(logger, p.tracerProvider, p.metricsProvider, p.pubsubClient, topic, handlerFunc)
	p.consumerCache[topic] = pub

	return pub, nil
}
