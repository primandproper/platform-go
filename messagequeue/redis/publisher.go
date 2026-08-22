package redis

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/redisclient"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/mqmetrics"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"github.com/redis/go-redis/v9"
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
		o11y        observability.Observer
		encoder     encoding.ClientEncoder
		publisher   messagePublisher
		instruments *mqmetrics.Publisher
		topic       string
	}
)

var _ messagequeue.Publisher = (*redisPublisher)(nil)

// Stop implements the Publisher interface. The underlying Redis client is shared
// across every topic publisher, so stopping one topic must not close it; the
// client is closed once by the provider's Close method.
func (p *redisPublisher) Stop() {}

// Publish implements the Publisher interface.
//
// Every messagequeue.PublishOption is accepted and ignored. Redis pub/sub has
// nothing to map an ordering key onto — no partitions, no consumer groups, no
// per-key sequencing — and nothing that deduplicates, so honoring either option
// would mean claiming a guarantee this backend cannot keep. A caller that needs
// ordering needs a different backend, not a different call.
func (p *redisPublisher) Publish(ctx context.Context, data any, _ ...messagequeue.PublishOption) error {
	ctx, op := p.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()

	var b bytes.Buffer
	if err := p.encoder.Encode(ctx, &b, data); err != nil {
		p.instruments.Failed(ctx)
		return op.Error(err, "encoding topic message")
	}

	op.Set(keys.LengthKey, b.Len())

	if err := p.publisher.Publish(ctx, p.topic, b.Bytes()).Err(); err != nil {
		p.instruments.Failed(ctx)
		// The encode failure two lines up goes through op.Error and this one used
		// to return bare, so a span for a publish that never reached Redis ended
		// green while a span for one that failed to serialize ended red.
		return op.Error(err, "publishing message to topic")
	}

	p.instruments.Published(ctx, startTime)

	return nil
}

// PublishAsync implements the Publisher interface. Like Publish, it accepts
// every messagequeue.PublishOption and honors none.
func (p *redisPublisher) PublishAsync(ctx context.Context, data any, _ ...messagequeue.PublishOption) {
	if err := p.Publish(ctx, data); err != nil {
		p.o11y.Logger().Error("publishing message", err)
	}
}

// provideRedisPublisher provides a redis-backed Publisher.
func provideRedisPublisher(logger logging.Logger, tracerProvider tracing.Provider, metricsProvider metrics.Provider, redisClient messagePublisher, topic string) (*redisPublisher, error) {
	instruments, err := mqmetrics.NewPublisher(metricsProvider, topic)
	if err != nil {
		return nil, err
	}

	return &redisPublisher{
		publisher:   redisClient,
		topic:       topic,
		encoder:     encoding.NewClientEncoder(encoding.ContentTypeJSON, encoding.WithLogger(logger), encoding.WithTracerProvider(tracerProvider)),
		o11y:        observability.NewObserverWithValues(fmt.Sprintf("%s_publisher", topic), logger, tracerProvider, map[string]any{keys.TopicKey: topic}),
		instruments: instruments,
	}, nil
}

var _ messagequeue.PublisherProvider = (*PublisherProvider)(nil)

// PublisherProvider is the Redis messagequeue.PublisherProvider implementation. It is
// exported, and returned by NewRedisPublisherProvider, so a caller who has chosen
// Redis can depend on that choice rather than on the interface every
// broker shares.
type PublisherProvider struct {
	o11y              observability.Observer
	publisherCache    map[string]messagequeue.Publisher
	redisClient       messagePublisher
	tracerProvider    tracing.Provider
	metricsProvider   metrics.Provider
	publisherCacheHat sync.RWMutex
}

// NewRedisPublisherProvider returns a PublisherProvider for a given address.
//
// It takes a context and reports an error so that the config's own
// ValidateWithContext runs here, and so that an address list redisclient
// refuses is a startup error. Without either, a config naming no
// QueueAddresses built cleanly and the provider came back holding a nil
// client — which nothing noticed until the first Publish panicked, in
// whichever goroutine happened to make it.
func NewRedisPublisherProvider(ctx context.Context, cfg Config, opts ...Option) (*PublisherProvider, error) {
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating redis publisher config")
	}

	o := newOptions(opts)
	o11y := observability.NewObserver("redis_publisher_provider", o.logger, o.tracerProvider)
	logger := o11y.Logger().WithValue("queue_addresses", cfg.QueueAddresses).
		WithValue(keys.UsernameKey, cfg.Username).
		WithValue("password_empty", cfg.Password == "")
	logger.Info("setting up redis publisher")

	client, err := redisclient.New(redisclient.Config{
		Username:  cfg.Username,
		Password:  cfg.Password,
		Addresses: cfg.QueueAddresses,
	})
	if err != nil {
		return nil, platformerrors.Wrap(err, "building redis client")
	}

	logger.Info("redis publisher setup complete")

	return &PublisherProvider{
		o11y:            o11y,
		redisClient:     client,
		publisherCache:  map[string]messagequeue.Publisher{},
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
	}, nil
}

// NewPublisher returns a Publisher for a given topic.
func (p *PublisherProvider) NewPublisher(ctx context.Context, topic string) (messagequeue.Publisher, error) {
	if topic == "" {
		return nil, messagequeue.ErrEmptyTopicName
	}

	logger := p.o11y.Logger().WithValue(keys.TopicKey, topic)

	p.publisherCacheHat.Lock()
	defer p.publisherCacheHat.Unlock()
	if cachedPub, ok := p.publisherCache[topic]; ok {
		return cachedPub, nil
	}

	pub, err := provideRedisPublisher(logger, p.tracerProvider, p.metricsProvider, p.redisClient, topic)
	if err != nil {
		return nil, err
	}
	p.publisherCache[topic] = pub

	return pub, nil
}

// Ping pings the underlying Redis client.
func (p *PublisherProvider) Ping(ctx context.Context) error {
	return p.redisClient.Ping(ctx).Err()
}

// Close closes the publisher.
func (p *PublisherProvider) Close() {
	if err := p.redisClient.Close(); err != nil {
		p.o11y.Logger().Error("closing redis publisher", err)
	}
}
