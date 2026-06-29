package redis

import (
	"context"
	"fmt"
	"sync"

	"github.com/primandproper/platform-go/v2/messagequeue"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	"github.com/redis/go-redis/v9"
)

type (
	subscriptionProvider interface {
		Subscribe(ctx context.Context, channels ...string) *redis.PubSub
	}

	channelProvider interface {
		Channel(...redis.ChannelOption) <-chan *redis.Message
	}

	redisConsumer struct {
		o11y            observability.Observer
		consumedCounter metrics.Int64Counter
		handlerFunc     func(context.Context, []byte) error
		subscription    channelProvider
	}
)

func provideRedisConsumer(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, redisClient subscriptionProvider, topic string, handlerFunc func(context.Context, []byte) error) (*redisConsumer, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	consumedCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_consumed", topic))
	if err != nil {
		panic(fmt.Sprintf("creating consumed counter: %v", err))
	}

	subscription := redisClient.Subscribe(ctx, topic)

	// Block until Redis confirms the SUBSCRIBE has been registered on the
	// server. Without this, a publisher racing us would silently drop the
	// first message — Redis pub/sub does not buffer for late subscribers.
	// See go-redis's own Subscribe doc comment for the rationale.
	if _, err = subscription.Receive(ctx); err != nil {
		return nil, fmt.Errorf("confirming redis subscription to %q: %w", topic, err)
	}

	logger.Debug("subscribed to topic!")

	return &redisConsumer{
		handlerFunc:     handlerFunc,
		subscription:    subscription,
		o11y:            observability.NewObserver(fmt.Sprintf("%s_consumer", topic), logger, tracerProvider),
		consumedCounter: consumedCounter,
	}, nil
}

// Consume reads messages and applies the handler to their payloads.
// Writes errors to the error chan if it isn't nil.
func (r *redisConsumer) Consume(ctx context.Context, stopChan chan bool, errs chan error) {
	if stopChan == nil {
		stopChan = make(chan bool, 1)
	}
	subChan := r.subscription.Channel()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-subChan:
			msgCtx, op := r.o11y.BeginCustom(ctx, "consume_message")
			op.Set(keys.TopicKey, msg.Channel).Set(keys.LengthKey, len(msg.Payload))
			r.consumedCounter.Add(msgCtx, 1)
			if err := r.handlerFunc(msgCtx, []byte(msg.Payload)); err != nil {
				op.Acknowledge(err, "handling message")
				if errs != nil {
					errs <- err
				}
			}
			op.End()
		case <-stopChan:
			return
		}
	}
}

type consumerProvider struct {
	o11y            observability.Observer
	tracerProvider  tracing.TracerProvider
	metricsProvider metrics.Provider
	consumerCache   map[string]messagequeue.Consumer
	redisClient     subscriptionProvider
	consumerCacheMu sync.RWMutex
}

// ProvideRedisConsumerProvider returns a ConsumerProvider for a given address.
func ProvideRedisConsumerProvider(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, cfg Config) messagequeue.ConsumerProvider {
	o11y := observability.NewObserver("redis_consumer_provider", logger, tracerProvider)
	o11y.Logger().WithValue("queue_addresses", cfg.QueueAddresses).
		WithValue(keys.UsernameKey, cfg.Username).
		WithValue("password_empty", cfg.Password == "").Info("setting up redis consumer")

	var redisClient subscriptionProvider
	if len(cfg.QueueAddresses) > 1 {
		redisClient = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:    cfg.QueueAddresses,
			Username: cfg.Username,
			Password: cfg.Password,
		})
	} else if len(cfg.QueueAddresses) == 1 {
		redisClient = redis.NewClient(&redis.Options{
			Addr:     cfg.QueueAddresses[0],
			Username: cfg.Username,
			Password: cfg.Password,
		})
	}

	return &consumerProvider{
		o11y:            o11y,
		tracerProvider:  tracerProvider,
		metricsProvider: metricsProvider,
		redisClient:     redisClient,
		consumerCache:   map[string]messagequeue.Consumer{},
	}
}

// ProvideConsumer returns a Consumer for a given topic.
func (p *consumerProvider) ProvideConsumer(ctx context.Context, topic string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	logger := p.o11y.Logger().WithValue(keys.TopicKey, topic)

	if topic == "" {
		return nil, ErrEmptyInputProvided
	}

	p.consumerCacheMu.RLock()
	if cachedPub, ok := p.consumerCache[topic]; ok {
		p.consumerCacheMu.RUnlock()
		return cachedPub, nil
	}
	p.consumerCacheMu.RUnlock()

	// Build the consumer outside the cache lock — provideRedisConsumer now
	// does a network RTT waiting for SUBSCRIBE confirmation, and we don't
	// want to serialize that behind the mutex.
	c, err := provideRedisConsumer(ctx, logger, p.tracerProvider, p.metricsProvider, p.redisClient, topic, handlerFunc)
	if err != nil {
		return nil, err
	}

	p.consumerCacheMu.Lock()
	defer p.consumerCacheMu.Unlock()
	// Re-check in case a concurrent caller beat us to it.
	if cachedPub, ok := p.consumerCache[topic]; ok {
		return cachedPub, nil
	}
	p.consumerCache[topic] = c

	return c, nil
}
