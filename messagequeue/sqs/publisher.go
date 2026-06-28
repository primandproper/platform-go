package sqs

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/primandproper/platform-go/encoding"
	"github.com/primandproper/platform-go/messagequeue"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type (
	messagePublisher interface {
		SendMessage(ctx context.Context, input *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	}

	sqsPublisher struct {
		o11y              observability.Observer
		encoder           encoding.ClientEncoder
		publisher         messagePublisher
		publishedCounter  metrics.Int64Counter
		publishErrCounter metrics.Int64Counter
		latencyHist       metrics.Float64Histogram
		topic             string
	}
)

// Stop does nothing.
func (p *sqsPublisher) Stop() {}

// Publish publishes a message onto an SQS event queue.
func (p *sqsPublisher) Publish(ctx context.Context, data any) error {
	ctx, op := p.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()

	op.Set(keys.TopicKey, p.topic)
	op.Logger().Debug("publishing message")

	var b bytes.Buffer
	if err := p.encoder.Encode(ctx, &b, data); err != nil {
		p.publishErrCounter.Add(ctx, 1)
		return observability.PrepareError(err, op.Span(), "encoding topic message")
	}

	op.Set(keys.LengthKey, b.Len())

	input := &sqs.SendMessageInput{
		MessageBody: aws.String(b.String()),
		QueueUrl:    aws.String(p.topic),
	}

	if _, err := p.publisher.SendMessage(ctx, input); err != nil {
		p.publishErrCounter.Add(ctx, 1)
		return observability.PrepareError(err, op.Span(), "publishing message")
	}

	p.publishedCounter.Add(ctx, 1)
	p.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))

	return nil
}

// PublishAsync publishes a message onto an SQS event queue.
func (p *sqsPublisher) PublishAsync(ctx context.Context, data any) {
	if err := p.Publish(ctx, data); err != nil {
		p.o11y.Logger().Error("publishing message", err)
	}
}

// provideSQSPublisher provides a sqs-backed Publisher.
func provideSQSPublisher(logger logging.Logger, sqsClient messagePublisher, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, topic string) *sqsPublisher {
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

	return &sqsPublisher{
		publisher:         sqsClient,
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
	sqsClient         messagePublisher
	tracerProvider    tracing.TracerProvider
	metricsProvider   metrics.Provider
	publisherCacheHat sync.RWMutex
}

// ProvideSQSPublisherProvider returns a PublisherProvider for a given address.
func ProvideSQSPublisherProvider(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) messagequeue.PublisherProvider {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		panic("sqs publisher provider: load default config: " + err.Error())
	}
	svc := sqs.NewFromConfig(cfg)

	return &publisherProvider{
		o11y:            observability.NewObserver("sqs_publisher_provider", logger, tracerProvider),
		sqsClient:       svc,
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

	pub := provideSQSPublisher(logger, p.sqsClient, p.tracerProvider, p.metricsProvider, topic)
	p.publisherCache[topic] = pub

	return pub, nil
}

// Ping is a no-op for SQS (SQS is a managed service).
func (p *publisherProvider) Ping(context.Context) error { return nil }

// Close returns a Publisher for a given topic.
func (p *publisherProvider) Close() {}
