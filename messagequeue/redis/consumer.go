package redis

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/redisclient"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/consumererr"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/mqmetrics"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/panicking"

	"github.com/redis/go-redis/v9"
)

type (
	subscriptionProvider interface {
		Subscribe(ctx context.Context, channels ...string) *redis.PubSub
	}

	channelProvider interface {
		Channel(...redis.ChannelOption) <-chan *redis.Message
		Close() error
	}

	redisConsumer struct {
		o11y         observability.Observer
		instruments  *mqmetrics.Consumer
		handlerFunc  func(context.Context, []byte) error
		subscription channelProvider
	}
)

func provideRedisConsumer(ctx context.Context, logger logging.Logger, tracerProvider tracing.Provider, metricsProvider metrics.Provider, redisClient subscriptionProvider, topic string, handlerFunc func(context.Context, []byte) error) (*redisConsumer, error) {
	instruments, err := mqmetrics.NewConsumer(metricsProvider, topic)
	if err != nil {
		return nil, err
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
		handlerFunc:  handlerFunc,
		subscription: subscription,
		o11y:         observability.NewObserverWithValues(fmt.Sprintf("%s_consumer", topic), logger, tracerProvider, map[string]any{keys.TopicKey: topic}),
		instruments:  instruments,
	}, nil
}

// Consume reads messages and applies the handler to their payloads.
// Writes errors to the error chan if it isn't nil.
func (r *redisConsumer) Consume(ctx context.Context, errs chan<- error) {
	// Closing the subscription on exit unsubscribes from the topic and releases the
	// server-side subscription rather than leaking it.
	defer func() {
		if err := r.subscription.Close(); err != nil {
			r.o11y.Logger().Error("closing redis subscription", err)
		}
	}()

	subChan := r.subscription.Channel()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-subChan:
			if !ok {
				// go-redis closes this channel when the PubSub is closed; a receive on a
				// closed channel yields a nil *Message that would panic on msg.Channel.
				return
			}
			msgCtx, op := r.o11y.BeginCustom(ctx, "consume_message")
			op.Set(keys.LengthKey, len(msg.Payload))

			startedAt := time.Now()
			err := panicking.Contain(func() error { return r.handlerFunc(msgCtx, []byte(msg.Payload)) })
			r.instruments.Handled(msgCtx, startedAt, err)

			if err != nil {
				op.Acknowledge(err, "handling message")
				consumererr.Send(msgCtx, errs, err)
			}
			op.End()
		}
	}
}

var _ messagequeue.ConsumerProvider = (*ConsumerProvider)(nil)

// ConsumerProvider is the Redis messagequeue.ConsumerProvider implementation. It is
// exported, and returned by NewRedisConsumerProvider, so a caller who has chosen
// Redis can depend on that choice rather than on the interface every
// broker shares.
type ConsumerProvider struct {
	o11y            observability.Observer
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	consumerCache   map[string]messagequeue.Consumer
	redisClient     subscriptionProvider
	consumerCacheMu sync.RWMutex
}

// NewRedisConsumerProvider returns a ConsumerProvider for a given address.
//
// It takes a context and reports an error so that the config's own
// ValidateWithContext runs here, and so that an address list redisclient
// refuses is a startup error. Without either, a config naming no
// QueueAddresses built cleanly and the provider came back holding a nil
// client — which nothing noticed until the first Ping panicked.
func NewRedisConsumerProvider(ctx context.Context, cfg Config, opts ...Option) (*ConsumerProvider, error) {
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating redis consumer config")
	}

	o := newOptions(opts)
	o11y := observability.NewObserver("redis_consumer_provider", o.logger, o.tracerProvider)
	o11y.Logger().WithValue("queue_addresses", cfg.QueueAddresses).
		WithValue(keys.UsernameKey, cfg.Username).
		WithValue("password_empty", cfg.Password == "").Info("setting up redis consumer")

	client, err := redisclient.New(redisclient.Config{
		Username:  cfg.Username,
		Password:  cfg.Password,
		Addresses: cfg.QueueAddresses,
	})
	if err != nil {
		return nil, platformerrors.Wrap(err, "building redis client")
	}

	return &ConsumerProvider{
		o11y:            o11y,
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
		redisClient:     client,
		consumerCache:   map[string]messagequeue.Consumer{},
	}, nil
}

// Close closes the shared Redis client, mirroring the publisher provider. Cached
// consumers close their own subscriptions when their Consume loops exit.
func (p *ConsumerProvider) Close() {
	if closer, ok := p.redisClient.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			p.o11y.Logger().Error("closing redis consumer client", err)
		}
	}
}

// NewConsumer returns a Consumer for a given topic.
func (p *ConsumerProvider) NewConsumer(ctx context.Context, topic string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	logger := p.o11y.Logger().WithValue(keys.TopicKey, topic)

	if topic == "" {
		return nil, messagequeue.ErrEmptyTopicName
	}

	p.consumerCacheMu.RLock()
	_, exists := p.consumerCache[topic]
	p.consumerCacheMu.RUnlock()

	if exists {
		return nil, platformerrors.Wrapf(messagequeue.ErrConsumerAlreadyRegistered, "topic %q", topic)
	}

	// Build the consumer outside the cache lock — provideRedisConsumer now
	// does a network RTT waiting for SUBSCRIBE confirmation, and we don't
	// want to serialize that behind the mutex.
	c, err := provideRedisConsumer(ctx, logger, p.tracerProvider, p.metricsProvider, p.redisClient, topic, handlerFunc)
	if err != nil {
		return nil, err
	}

	p.consumerCacheMu.Lock()
	defer p.consumerCacheMu.Unlock()
	// Re-check in case a concurrent caller beat us to it. If so, close the
	// subscription we just opened so the losing racer's live subscription doesn't leak.
	if _, ok := p.consumerCache[topic]; ok {
		if err = c.subscription.Close(); err != nil {
			logger.Error("closing redundant redis subscription", err)
		}

		return nil, platformerrors.Wrapf(messagequeue.ErrConsumerAlreadyRegistered, "topic %q", topic)
	}
	p.consumerCache[topic] = c

	return c, nil
}
