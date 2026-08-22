package sqs

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/mqmetrics"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type (
	messagePublisher interface {
		SendMessage(ctx context.Context, input *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	}

	sqsPublisher struct {
		o11y        observability.Observer
		encoder     encoding.ClientEncoder
		publisher   messagePublisher
		instruments *mqmetrics.Publisher
		topic       string
	}
)

var _ messagequeue.Publisher = (*sqsPublisher)(nil)

// Stop does nothing.
func (p *sqsPublisher) Stop() {}

// Publish publishes a message onto an SQS event queue.
//
// messagequeue.WithOrderingKey becomes the message's MessageGroupId, which a
// FIFO queue requires on every message and which a standard queue reads as a
// fair-queue tenant tag. messagequeue.WithDeduplicationKey becomes its
// MessageDeduplicationId, which a FIFO queue also requires unless the queue has
// ContentBasedDeduplication enabled. Neither is sent when its option is absent,
// which is what a standard queue wants.
func (p *sqsPublisher) Publish(ctx context.Context, data any, opts ...messagequeue.PublishOption) error {
	ctx, op := p.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()

	op.Logger().Debug("publishing message")

	o := messagequeue.NewPublishOptions(opts...)

	var b bytes.Buffer
	if err := p.encoder.Encode(ctx, &b, data); err != nil {
		p.instruments.Failed(ctx)
		return observability.PrepareError(err, op.Span(), "encoding topic message")
	}

	op.Set(keys.LengthKey, b.Len())

	input := &sqs.SendMessageInput{
		MessageBody: aws.String(b.String()),
		QueueUrl:    aws.String(p.topic),
	}

	// Absent has to stay a nil pointer: the SDK omits a nil field from the
	// request entirely and serializes a pointer to "" as a present, empty
	// parameter, which is not the same thing to a queue that reads either field.
	if o.OrderingKey != "" {
		input.MessageGroupId = aws.String(o.OrderingKey)
		op.SpanOnly(messagequeue.OrderingKeyAttribute, o.OrderingKey)
	}

	if o.DeduplicationKey != "" {
		input.MessageDeduplicationId = aws.String(o.DeduplicationKey)
	}

	if _, err := p.publisher.SendMessage(ctx, input); err != nil {
		p.instruments.Failed(ctx)
		return observability.PrepareError(err, op.Span(), "publishing message")
	}

	p.instruments.Published(ctx, startTime)

	return nil
}

// PublishAsync publishes a message onto an SQS event queue.
func (p *sqsPublisher) PublishAsync(ctx context.Context, data any, opts ...messagequeue.PublishOption) {
	if err := p.Publish(ctx, data, opts...); err != nil {
		p.o11y.Logger().Error("publishing message", err)
	}
}

// provideSQSPublisher provides a sqs-backed Publisher.
func provideSQSPublisher(logger logging.Logger, sqsClient messagePublisher, tracerProvider tracing.Provider, metricsProvider metrics.Provider, topic string) (*sqsPublisher, error) {
	instruments, err := mqmetrics.NewPublisher(metricsProvider, topic)
	if err != nil {
		return nil, err
	}

	return &sqsPublisher{
		publisher:   sqsClient,
		topic:       topic,
		encoder:     encoding.NewClientEncoder(encoding.ContentTypeJSON, encoding.WithLogger(logger), encoding.WithTracerProvider(tracerProvider)),
		o11y:        observability.NewObserverWithValues(fmt.Sprintf("%s_publisher", topic), logger, tracerProvider, map[string]any{keys.TopicKey: topic}),
		instruments: instruments,
	}, nil
}

var _ messagequeue.PublisherProvider = (*PublisherProvider)(nil)

// PublisherProvider is the Amazon SQS messagequeue.PublisherProvider implementation. It is
// exported, and returned by NewSQSPublisherProvider, so a caller who has chosen
// Amazon SQS can depend on that choice rather than on the interface every
// broker shares.
type PublisherProvider struct {
	o11y              observability.Observer
	publisherCache    map[string]messagequeue.Publisher
	sqsClient         messagePublisher
	tracerProvider    tracing.Provider
	metricsProvider   metrics.Provider
	publisherCacheHat sync.RWMutex
}

// NewSQSPublisherProvider returns a PublisherProvider for a given address.
func NewSQSPublisherProvider(ctx context.Context, queueCfg Config, opts ...Option) (*PublisherProvider, error) {
	o := newOptions(opts)

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

	return &PublisherProvider{
		o11y:            observability.NewObserver("sqs_publisher_provider", o.logger, o.tracerProvider),
		sqsClient:       svc,
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

	pub, err := provideSQSPublisher(logger, p.sqsClient, p.tracerProvider, p.metricsProvider, topic)
	if err != nil {
		return nil, err
	}
	p.publisherCache[topic] = pub

	return pub, nil
}

// Ping is a no-op for SQS (SQS is a managed service).
func (p *PublisherProvider) Ping(context.Context) error { return nil }

// Close is a no-op: the SQS client is a stateless HTTP client with nothing to
// release.
func (p *PublisherProvider) Close() {}
