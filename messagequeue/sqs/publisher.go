package sqs

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v4/encoding"
	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/messagequeue"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/keys"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"

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
func provideSQSPublisher(logger logging.Logger, sqsClient messagePublisher, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, topic string) (*sqsPublisher, error) {
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

	return &sqsPublisher{
		publisher:         sqsClient,
		topic:             topic,
		encoder:           encoding.NewClientEncoder(logger, tracerProvider, encoding.ContentTypeJSON),
		o11y:              observability.NewObserver(fmt.Sprintf("%s_publisher", topic), logger, tracerProvider),
		publishedCounter:  publishedCounter,
		publishErrCounter: publishErrCounter,
		latencyHist:       latencyHist,
	}, nil
}

type publisherProvider struct {
	o11y              observability.Observer
	publisherCache    map[string]messagequeue.Publisher
	sqsClient         messagePublisher
	tracerProvider    tracing.TracerProvider
	metricsProvider   metrics.Provider
	publisherCacheHat sync.RWMutex
}

// NewSQSPublisherProvider returns a PublisherProvider for a given address.
func NewSQSPublisherProvider(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, queueCfg Config) (messagequeue.PublisherProvider, error) {
	var loadOpts []func(*config.LoadOptions) error
	if queueCfg.QueueAddress != "" {
		// Override the AWS endpoint (e.g. to point at localstack) when configured,
		// mirroring the consumer provider.
		loadOpts = append(loadOpts, config.WithBaseEndpoint(queueCfg.QueueAddress))
	}

	cfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		// Return the error instead of panicking, matching the consumer twin
		// (NewSQSConsumerProvider) so a config-load failure is a handleable error
		// rather than a crash.
		return nil, errors.Wrap(err, "loading default AWS config")
	}
	svc := sqs.NewFromConfig(cfg)

	return &publisherProvider{
		o11y:            observability.NewObserver("sqs_publisher_provider", logger, tracerProvider),
		sqsClient:       svc,
		publisherCache:  map[string]messagequeue.Publisher{},
		tracerProvider:  tracerProvider,
		metricsProvider: metricsProvider,
	}, nil
}

// NewPublisher returns a Publisher for a given topic.
func (p *publisherProvider) NewPublisher(ctx context.Context, topic string) (messagequeue.Publisher, error) {
	if topic == "" {
		return nil, messagequeue.ErrEmptyTopicName
	}
	logger := p.o11y.Logger().WithValue(keys.TopicKey, topic)

	p.publisherCacheHat.Lock()
	defer p.publisherCacheHat.Unlock()
	if cachedPub, ok := p.publisherCache[topic]; ok {
		return cachedPub, nil
	}

	pub, err := provideSQSPublisher(logger, p.sqsClient, p.tracerProvider, p.metricsProvider, topic)
	if err != nil {
		return nil, err
	}
	p.publisherCache[topic] = pub

	return pub, nil
}

// Ping is a no-op for SQS (SQS is a managed service).
func (p *publisherProvider) Ping(context.Context) error { return nil }

// Close returns a Publisher for a given topic.
func (p *publisherProvider) Close() {}
