package kafka

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v4/encoding"
	"github.com/primandproper/platform-go/v4/messagequeue"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/keys"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/segmentio/kafka-go"
)

type (
	kafkaWriter interface {
		WriteMessages(ctx context.Context, msgs ...kafka.Message) error
		Close() error
	}

	kafkaPublisher struct {
		o11y              observability.Observer
		encoder           encoding.ClientEncoder
		writer            kafkaWriter
		publishedCounter  metrics.Int64Counter
		publishErrCounter metrics.Int64Counter
		latencyHist       metrics.Float64Histogram
		topic             string
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
func (p *kafkaPublisher) Publish(ctx context.Context, data any) error {
	ctx, op := p.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.TopicKey, p.topic)

	startTime := time.Now()

	var b bytes.Buffer
	if err := p.encoder.Encode(ctx, &b, data); err != nil {
		p.publishErrCounter.Add(ctx, 1)
		return op.Error(err, "encoding topic message")
	}

	op.Set(keys.LengthKey, b.Len())

	if err := p.writer.WriteMessages(ctx, kafka.Message{Value: b.Bytes()}); err != nil {
		p.publishErrCounter.Add(ctx, 1)
		return op.Error(err, "publishing message")
	}

	p.publishedCounter.Add(ctx, 1)
	p.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))

	return nil
}

// PublishAsync publishes a message to a Kafka topic without waiting for acknowledgement.
func (p *kafkaPublisher) PublishAsync(ctx context.Context, data any) {
	if err := p.Publish(ctx, data); err != nil {
		p.o11y.Logger().Error("publishing message", err)
	}
}

func provideKafkaPublisher(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, brokers []string, topic string) (*kafkaPublisher, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	publishedCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_published", topic))
	if err != nil {
		return nil, fmt.Errorf("creating published counter: %w", err)
	}

	publishErrCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_publish_errors", topic))
	if err != nil {
		return nil, fmt.Errorf("creating publish error counter: %w", err)
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_publish_latency_ms", topic))
	if err != nil {
		return nil, fmt.Errorf("creating publish latency histogram: %w", err)
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
	}

	return &kafkaPublisher{
		writer:            writer,
		encoder:           encoding.NewClientEncoder(logger, tracerProvider, encoding.ContentTypeJSON),
		o11y:              observability.NewObserver(fmt.Sprintf("%s_publisher", topic), logger, tracerProvider),
		topic:             topic,
		publishedCounter:  publishedCounter,
		publishErrCounter: publishErrCounter,
		latencyHist:       latencyHist,
	}, nil
}

type publisherProvider struct {
	logger            logging.Logger
	publisherCache    map[string]messagequeue.Publisher
	tracerProvider    tracing.TracerProvider
	metricsProvider   metrics.Provider
	brokers           []string
	publisherCacheHat sync.RWMutex
}

var _ messagequeue.PublisherProvider = (*publisherProvider)(nil)

// NewKafkaPublisherProvider returns a PublisherProvider backed by Kafka.
func NewKafkaPublisherProvider(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, cfg Config) messagequeue.PublisherProvider {
	logger.WithValue("brokers", cfg.Brokers).Info("setting up kafka publisher")

	return &publisherProvider{
		logger:          logging.EnsureLogger(logger),
		brokers:         cfg.Brokers,
		publisherCache:  map[string]messagequeue.Publisher{},
		tracerProvider:  tracerProvider,
		metricsProvider: metricsProvider,
	}
}

// NewPublisher returns a Publisher for the given topic.
func (p *publisherProvider) NewPublisher(_ context.Context, topic string) (messagequeue.Publisher, error) {
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
func (p *publisherProvider) Ping(ctx context.Context) error {
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
func (p *publisherProvider) Close() {
	p.publisherCacheHat.Lock()
	defer p.publisherCacheHat.Unlock()
	for _, pub := range p.publisherCache {
		pub.Stop()
	}
}
