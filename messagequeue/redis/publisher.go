package redis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/primandproper/platform-go/encoding"
	platformerrors "github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/messagequeue"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/redis/go-redis/v9"
)

var (
	// ErrEmptyInputProvided indicates empty input was provided in an unacceptable context.
	ErrEmptyInputProvided = platformerrors.New("empty input provided")
)

var _ messagePublisher = (*redis.ClusterClient)(nil)

type (
	messagePinger interface {
		Ping(ctx context.Context) *redis.StatusCmd
	}

	messagePublisher interface {
		io.Closer
		messagePinger
		Publish(ctx context.Context, channel string, message any) *redis.IntCmd
	}

	redisPublisher struct {
		o11y              observability.Observer
		encoder           encoding.ClientEncoder
		publisher         messagePublisher
		publishedCounter  metrics.Int64Counter
		publishErrCounter metrics.Int64Counter
		latencyHist       metrics.Float64Histogram
		topic             string
	}
)

// Stop implements the Publisher interface.
func (p *redisPublisher) Stop() {
	if err := p.publisher.Close(); err != nil && !errors.Is(err, redis.ErrClosed) {
		p.o11y.Logger().Error("closing redis publisher", err)
	}
}

// Publish implements the Publisher interface.
func (p *redisPublisher) Publish(ctx context.Context, data any) error {
	ctx, op := p.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()

	var b bytes.Buffer
	if err := p.encoder.Encode(ctx, &b, data); err != nil {
		p.publishErrCounter.Add(ctx, 1)
		return op.Error(err, "encoding topic message")
	}

	op.Set(keys.TopicKey, p.topic).Set(keys.LengthKey, b.Len())

	if err := p.publisher.Publish(ctx, p.topic, b.Bytes()).Err(); err != nil {
		p.publishErrCounter.Add(ctx, 1)
		return err
	}

	p.publishedCounter.Add(ctx, 1)
	p.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))

	return nil
}

// PublishAsync implements the Publisher interface.
func (p *redisPublisher) PublishAsync(ctx context.Context, data any) {
	if err := p.Publish(ctx, data); err != nil {
		p.o11y.Logger().Error("publishing message", err)
	}
}

// provideRedisPublisher provides a redis-backed Publisher.
func provideRedisPublisher(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, redisClient messagePublisher, topic string) *redisPublisher {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	publishedCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_published", topic))
	if err != nil {
		panic(fmt.Sprintf("creating published counter: %v", err))
	}

	publishErrCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_publish_errors", topic))
	if err != nil {
		panic(fmt.Sprintf("creating publish error counter: %v", err))
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_publish_latency_ms", topic))
	if err != nil {
		panic(fmt.Sprintf("creating publish latency histogram: %v", err))
	}

	return &redisPublisher{
		publisher:         redisClient,
		topic:             topic,
		encoder:           encoding.ProvideClientEncoder(logger, tracerProvider, encoding.ContentTypeJSON),
		o11y:              observability.NewObserver(fmt.Sprintf("%s_publisher", topic), logger, tracerProvider),
		publishedCounter:  publishedCounter,
		publishErrCounter: publishErrCounter,
		latencyHist:       latencyHist,
	}
}

type publisherProvider struct {
	o11y              observability.Observer
	publisherCache    map[string]messagequeue.Publisher
	redisClient       messagePublisher
	tracerProvider    tracing.TracerProvider
	metricsProvider   metrics.Provider
	publisherCacheHat sync.RWMutex
}

// ProvideRedisPublisherProvider returns a PublisherProvider for a given address.
func ProvideRedisPublisherProvider(l logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, cfg Config) messagequeue.PublisherProvider {
	o11y := observability.NewObserver("redis_publisher_provider", l, tracerProvider)
	logger := o11y.Logger().WithValue("queue_addresses", cfg.QueueAddresses).
		WithValue(keys.UsernameKey, cfg.Username).
		WithValue("password_empty", cfg.Password == "")
	logger.Info("setting up redis publisher")

	var redisClient messagePublisher
	if len(cfg.QueueAddresses) > 1 {
		redisClient = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        cfg.QueueAddresses,
			Username:     cfg.Username,
			Password:     cfg.Password,
			DialTimeout:  1 * time.Second,
			WriteTimeout: 1 * time.Second,
		})
	} else if len(cfg.QueueAddresses) == 1 {
		redisClient = redis.NewClient(&redis.Options{
			Addr:         cfg.QueueAddresses[0],
			Username:     cfg.Username,
			Password:     cfg.Password,
			DialTimeout:  1 * time.Second,
			WriteTimeout: 1 * time.Second,
		})
	}

	logger.Info("redis publisher setup complete")

	return &publisherProvider{
		o11y:            o11y,
		redisClient:     redisClient,
		publisherCache:  map[string]messagequeue.Publisher{},
		tracerProvider:  tracerProvider,
		metricsProvider: metricsProvider,
	}
}

// ProvidePublisher returns a Publisher for a given topic.
func (p *publisherProvider) ProvidePublisher(ctx context.Context, topic string) (messagequeue.Publisher, error) {
	if topic == "" {
		return nil, messagequeue.ErrEmptyTopicName
	}

	logger := p.o11y.Logger().WithValue(keys.TopicKey, topic)

	p.publisherCacheHat.Lock()
	defer p.publisherCacheHat.Unlock()
	if cachedPub, ok := p.publisherCache[topic]; ok {
		return cachedPub, nil
	}

	pub := provideRedisPublisher(logger, p.tracerProvider, p.metricsProvider, p.redisClient, topic)
	p.publisherCache[topic] = pub

	return pub, nil
}

// Ping pings the underlying Redis client.
func (p *publisherProvider) Ping(ctx context.Context) error {
	return p.redisClient.Ping(ctx).Err()
}

// Close closes the publisher.
func (p *publisherProvider) Close() {
	if err := p.redisClient.Close(); err != nil {
		p.o11y.Logger().Error("closing redis publisher", err)
	}
}
