package pubsub

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/primandproper/platform-go/encoding"
	"github.com/primandproper/platform-go/messagequeue"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

	"cloud.google.com/go/pubsub/v2"
)

type (
	messagePublisher interface {
		Stop()
		Publish(context.Context, *pubsub.Message) *pubsub.PublishResult
	}

	pubSubPublisher struct {
		tracer            tracing.Tracer
		encoder           encoding.ClientEncoder
		logger            logging.Logger
		publisher         messagePublisher
		publishedCounter  metrics.Int64Counter
		publishErrCounter metrics.Int64Counter
		latencyHist       metrics.Float64Histogram
	}
)

// buildPubSubPublisher provides a Pub/Sub-backed pubSubPublisher.
func buildPubSubPublisher(logger logging.Logger, pubsubClient *pubsub.Publisher, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, topic string) *pubSubPublisher {
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

	return &pubSubPublisher{
		encoder:           encoding.ProvideClientEncoder(logger, tracerProvider, encoding.ContentTypeJSON),
		logger:            logging.EnsureLogger(logger),
		publisher:         pubsubClient,
		tracer:            tracing.NewNamedTracer(tracerProvider, fmt.Sprintf("%s_publisher", topic)),
		publishedCounter:  publishedCounter,
		publishErrCounter: publishErrCounter,
		latencyHist:       latencyHist,
	}
}

// Stop calls Stop on the topic.
func (p *pubSubPublisher) Stop() {
	p.publisher.Stop()
}

type publisherProvider struct {
	logger            logging.Logger
	publisherCache    map[string]messagequeue.Publisher
	pubsubClient      *pubsub.Client
	tracerProvider    tracing.TracerProvider
	metricsProvider   metrics.Provider
	projectID         string
	publisherCacheHat sync.RWMutex
}

// ProvidePubSubPublisherProvider returns a PublisherProvider for a given address.
func ProvidePubSubPublisherProvider(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, client *pubsub.Client, projectID string) messagequeue.PublisherProvider {
	return &publisherProvider{
		logger:          logging.EnsureLogger(logger),
		pubsubClient:    client,
		publisherCache:  map[string]messagequeue.Publisher{},
		tracerProvider:  tracerProvider,
		metricsProvider: metricsProvider,
		projectID:       projectID,
	}
}

// Ping is a no-op for GCP Pub/Sub (managed service).
func (p *publisherProvider) Ping(context.Context) error { return nil }

// Close closes the connection topic.
func (p *publisherProvider) Close() {
	if err := p.pubsubClient.Close(); err != nil {
		p.logger.Error("closing pubsub connection", err)
	}
}

// qualifyTopicName ensures the topic name is fully qualified (projects/{project}/topics/{topic}).
func (p *publisherProvider) qualifyTopicName(topicName string) string {
	if strings.HasPrefix(topicName, "projects/") {
		return topicName
	}
	return fmt.Sprintf("projects/%s/topics/%s", p.projectID, topicName)
}

// ProvidePublisher returns a pubSubPublisher for a given topic.
func (p *publisherProvider) ProvidePublisher(ctx context.Context, topicName string) (messagequeue.Publisher, error) {
	if topicName == "" {
		return nil, messagequeue.ErrEmptyTopicName
	}

	qualifiedName := p.qualifyTopicName(topicName)

	logger := logging.EnsureLogger(p.logger.Clone())

	p.publisherCacheHat.Lock()
	defer p.publisherCacheHat.Unlock()
	if cachedPub, ok := p.publisherCache[qualifiedName]; ok {
		return cachedPub, nil
	}

	// Use Publisher directly with the qualified topic name. This avoids needing
	// pubsub.topics.get (TopicAdminClient.GetTopic); pubsub.topics.publish is sufficient.
	publisher := p.pubsubClient.Publisher(qualifiedName)

	pub := buildPubSubPublisher(logger, publisher, p.tracerProvider, p.metricsProvider, qualifiedName)
	p.publisherCache[qualifiedName] = pub

	return pub, nil
}

func (p *pubSubPublisher) Publish(ctx context.Context, data any) error {
	_, span := p.tracer.StartSpan(ctx)
	defer span.End()

	startTime := time.Now()
	logger := p.logger.Clone()

	var b bytes.Buffer
	if err := p.encoder.Encode(ctx, &b, data); err != nil {
		p.publishErrCounter.Add(ctx, 1)
		return observability.PrepareError(err, span, "encoding topic message")
	}

	msg := &pubsub.Message{Data: b.Bytes()}
	result := p.publisher.Publish(ctx, msg)

	<-result.Ready()

	// The Get method blocks until a server-generated ID or an error is returned for the published message.
	if _, err := result.Get(ctx); err != nil {
		p.publishErrCounter.Add(ctx, 1)
		observability.AcknowledgeError(err, logger, span, "publishing pubsub message")
	}

	p.publishedCounter.Add(ctx, 1)
	p.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))

	logger.Debug("published message")

	return nil
}

func (p *pubSubPublisher) PublishAsync(ctx context.Context, data any) {
	if err := p.Publish(ctx, data); err != nil {
		p.logger.Error("publishing message", err)
	}
}
