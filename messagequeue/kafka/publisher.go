package kafka

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/mqmetrics"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"github.com/segmentio/kafka-go"
)

type (
	kafkaWriter interface {
		WriteMessages(ctx context.Context, msgs ...kafka.Message) error
		Close() error
	}

	kafkaPublisher struct {
		o11y        observability.Observer
		encoder     encoding.ClientEncoder
		writer      kafkaWriter
		instruments *mqmetrics.Publisher
		topic       string
	}
)

var _ messagequeue.Publisher = (*kafkaPublisher)(nil)

// Stop closes the underlying Kafka writer.
func (p *kafkaPublisher) Stop() {
	if err := p.writer.Close(); err != nil {
		p.o11y.Logger().Error("closing kafka writer", err)
	}
}

// Publish publishes a message to a Kafka topic.
//
// messagequeue.WithOrderingKey sets the message key, which the writer's
// balancer hashes into a partition, so every message for one key lands on one
// partition and Kafka's per-partition ordering carries. Without it the key is
// left nil and the balancer places the message on a partition at random.
func (p *kafkaPublisher) Publish(ctx context.Context, data any, opts ...messagequeue.PublishOption) error {
	ctx, op := p.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()

	o := messagequeue.NewPublishOptions(opts...)

	var b bytes.Buffer
	if err := p.encoder.Encode(ctx, &b, data); err != nil {
		p.instruments.Failed(ctx)
		return op.Error(err, "encoding topic message")
	}

	op.Set(keys.LengthKey, b.Len())

	msg := kafka.Message{Value: b.Bytes()}
	if o.OrderingKey != "" {
		// Only a non-empty key becomes a Kafka key. An empty, non-nil key is a
		// value as far as the balancer is concerned, and would hash every
		// unkeyed message onto one partition.
		msg.Key = []byte(o.OrderingKey)
		op.SpanOnly(messagequeue.OrderingKeyAttribute, o.OrderingKey)
	}

	if err := p.writer.WriteMessages(ctx, msg); err != nil {
		p.instruments.Failed(ctx)
		return op.Error(err, "publishing message")
	}

	p.instruments.Published(ctx, startTime)

	return nil
}

// PublishAsync publishes a message to a Kafka topic without waiting for acknowledgement.
func (p *kafkaPublisher) PublishAsync(ctx context.Context, data any, opts ...messagequeue.PublishOption) {
	if err := p.Publish(ctx, data, opts...); err != nil {
		p.o11y.Logger().Error("publishing message", err)
	}
}

func provideKafkaPublisher(logger logging.Logger, tracerProvider tracing.Provider, metricsProvider metrics.Provider, brokers []string, topic string) (*kafkaPublisher, error) {
	instruments, err := mqmetrics.NewPublisher(metricsProvider, topic)
	if err != nil {
		return nil, err
	}

	writer := &kafka.Writer{
		Addr:                   kafka.TCP(brokers...),
		Topic:                  topic,
		AllowAutoTopicCreation: true,
		RequiredAcks:           kafka.RequireAll,
		// Publish is synchronous and typically sends one message at a time; kafka-go's
		// default BatchTimeout is 1s, so each single Publish would otherwise block ~1s
		// waiting for the batch to flush. Keep it small to cut that latency floor.
		BatchTimeout: 10 * time.Millisecond,
		// The balancer has to be named for messagequeue.WithOrderingKey to mean
		// anything: kafka-go's default is RoundRobin, which ignores the message
		// key entirely, so a keyed message would still scatter across partitions.
		// Murmur2 with Consistent left false is librdkafka's "murmur2_random" and
		// hashes the same way as the Java producer, so a topic this package
		// writes to partitions the same as one any other client writes to. A nil
		// key — every publish that names no ordering key — takes the random path
		// instead of hashing, which keeps unkeyed traffic spread rather than
		// piled onto one partition.
		Balancer: &kafka.Murmur2Balancer{},
	}

	return &kafkaPublisher{
		writer:      writer,
		encoder:     encoding.NewClientEncoder(encoding.ContentTypeJSON, encoding.WithLogger(logger), encoding.WithTracerProvider(tracerProvider)),
		o11y:        observability.NewObserverWithValues(fmt.Sprintf("%s_publisher", topic), logger, tracerProvider, map[string]any{keys.TopicKey: topic}),
		topic:       topic,
		instruments: instruments,
	}, nil
}

// PublisherProvider is the Kafka messagequeue.PublisherProvider implementation. It is
// exported, and returned by NewKafkaPublisherProvider, so a caller who has chosen
// Kafka can depend on that choice rather than on the interface every
// broker shares.
type PublisherProvider struct {
	logger            logging.Logger
	publisherCache    map[string]messagequeue.Publisher
	tracerProvider    tracing.Provider
	metricsProvider   metrics.Provider
	brokers           []string
	publisherCacheHat sync.RWMutex
}

var _ messagequeue.PublisherProvider = (*PublisherProvider)(nil)

// NewKafkaPublisherProvider returns a PublisherProvider backed by Kafka.
func NewKafkaPublisherProvider(cfg Config, opts ...Option) *PublisherProvider {
	o := newOptions(opts)
	logger := logging.EnsureLogger(o.logger)
	logger.WithValue("brokers", cfg.Brokers).Info("setting up kafka publisher")

	return &PublisherProvider{
		logger:          logger,
		brokers:         cfg.Brokers,
		publisherCache:  map[string]messagequeue.Publisher{},
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
	}
}

// NewPublisher returns a Publisher for the given topic.
func (p *PublisherProvider) NewPublisher(_ context.Context, topic string) (messagequeue.Publisher, error) {
	if topic == "" {
		return nil, messagequeue.ErrEmptyTopicName
	}

	p.publisherCacheHat.Lock()
	defer p.publisherCacheHat.Unlock()
	if cached, ok := p.publisherCache[topic]; ok {
		return cached, nil
	}

	pub, err := provideKafkaPublisher(p.logger, p.tracerProvider, p.metricsProvider, p.brokers, topic)
	if err != nil {
		return nil, err
	}

	p.publisherCache[topic] = pub

	return pub, nil
}

// Ping checks connectivity by attempting to dial a broker.
func (p *PublisherProvider) Ping(ctx context.Context) error {
	if len(p.brokers) == 0 {
		return fmt.Errorf("no kafka brokers configured")
	}

	conn, err := kafka.DialContext(ctx, "tcp", p.brokers[0])
	if err != nil {
		return err
	}
	return conn.Close()
}

// Close closes all cached publishers.
func (p *PublisherProvider) Close() {
	p.publisherCacheHat.Lock()
	defer p.publisherCacheHat.Unlock()
	for _, pub := range p.publisherCache {
		pub.Stop()
	}
}
