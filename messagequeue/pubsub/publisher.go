package pubsub

import (
	"bytes"
	"context"
	"fmt"
	"strings"
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

	"cloud.google.com/go/pubsub/v2"
)

type (
	messagePublisher interface {
		Stop()
		Publish(context.Context, *pubsub.Message) *pubsub.PublishResult
		ResumePublish(orderingKey string)
	}

	pubSubPublisher struct {
		o11y        observability.Observer
		encoder     encoding.ClientEncoder
		publisher   messagePublisher
		instruments *mqmetrics.Publisher
	}
)

// buildPubSubPublisher provides a Pub/Sub-backed pubSubPublisher.
func buildPubSubPublisher(logger logging.Logger, pubsubClient *pubsub.Publisher, tracerProvider tracing.Provider, metricsProvider metrics.Provider, topic string) (*pubSubPublisher, error) {
	instruments, err := mqmetrics.NewPublisher(metricsProvider, topic)
	if err != nil {
		return nil, err
	}

	return &pubSubPublisher{
		encoder:     encoding.NewClientEncoder(encoding.ContentTypeJSON, encoding.WithLogger(logger), encoding.WithTracerProvider(tracerProvider)),
		o11y:        observability.NewObserverWithValues(fmt.Sprintf("%s_publisher", topic), logger, tracerProvider, map[string]any{keys.TopicKey: topic}),
		publisher:   pubsubClient,
		instruments: instruments,
	}, nil
}

var _ messagequeue.Publisher = (*pubSubPublisher)(nil)

// Stop calls Stop on the topic.
func (p *pubSubPublisher) Stop() {
	p.publisher.Stop()
}

var _ messagequeue.PublisherProvider = (*PublisherProvider)(nil)

// PublisherProvider is the Google Cloud Pub/Sub messagequeue.PublisherProvider implementation. It is
// exported, and returned by NewPubSubPublisherProvider, so a caller who has chosen
// Google Cloud Pub/Sub can depend on that choice rather than on the interface every
// broker shares.
type PublisherProvider struct {
	logger            logging.Logger
	publisherCache    map[string]messagequeue.Publisher
	pubsubClient      *pubsub.Client
	tracerProvider    tracing.Provider
	metricsProvider   metrics.Provider
	projectID         string
	publisherCacheHat sync.RWMutex
}

// NewPubSubPublisherProvider returns a PublisherProvider for a given address.
func NewPubSubPublisherProvider(client *pubsub.Client, projectID string, opts ...Option) *PublisherProvider {
	o := newOptions(opts)

	return &PublisherProvider{
		logger:          logging.EnsureLogger(o.logger),
		pubsubClient:    client,
		publisherCache:  map[string]messagequeue.Publisher{},
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
		projectID:       projectID,
	}
}

// Ping is a no-op for GCP Pub/Sub (managed service).
func (p *PublisherProvider) Ping(context.Context) error { return nil }

// Close closes the connection topic.
func (p *PublisherProvider) Close() {
	if err := p.pubsubClient.Close(); err != nil {
		p.logger.Error("closing pubsub connection", err)
	}
}

// qualifyTopicName ensures the topic name is fully qualified (projects/{project}/topics/{topic}).
func (p *PublisherProvider) qualifyTopicName(topicName string) string {
	if strings.HasPrefix(topicName, "projects/") {
		return topicName
	}
	return fmt.Sprintf("projects/%s/topics/%s", p.projectID, topicName)
}

// NewPublisher returns a pubSubPublisher for a given topic.
func (p *PublisherProvider) NewPublisher(ctx context.Context, topicName string) (messagequeue.Publisher, error) {
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

	// Without this the client rejects any message carrying an ordering key
	// before it reaches the wire, so messagequeue.WithOrderingKey would fail
	// rather than order. Turning it on costs nothing for messages that carry no
	// key: the client bundles those under the empty key, which it schedules
	// exactly as it does with ordering off.
	publisher.EnableMessageOrdering = true

	pub, err := buildPubSubPublisher(logger, publisher, p.tracerProvider, p.metricsProvider, qualifiedName)
	if err != nil {
		return nil, err
	}
	p.publisherCache[qualifiedName] = pub

	return pub, nil
}

// Publish publishes a message to the topic and blocks until the server has
// assigned it an ID.
//
// messagequeue.WithOrderingKey becomes the message's OrderingKey. Pub/Sub then
// holds the key's messages to the order they were published, provided the
// subscription reading them was created with message ordering enabled —
// provisioning that subscription is outside this package, like the subscription
// itself.
func (p *pubSubPublisher) Publish(ctx context.Context, data any, opts ...messagequeue.PublishOption) error {
	ctx, op := p.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()

	o := messagequeue.NewPublishOptions(opts...)

	var b bytes.Buffer
	if err := p.encoder.Encode(ctx, &b, data); err != nil {
		p.instruments.Failed(ctx)
		return observability.PrepareError(err, op.Span(), "encoding topic message")
	}

	op.Set(keys.LengthKey, b.Len())

	msg := &pubsub.Message{Data: b.Bytes(), OrderingKey: o.OrderingKey}
	if o.OrderingKey != "" {
		op.SpanOnly(messagequeue.OrderingKeyAttribute, o.OrderingKey)
	}

	result := p.publisher.Publish(ctx, msg)

	<-result.Ready()

	// The Get method blocks until a server-generated ID or an error is returned for the published message.
	serverID, err := result.Get(ctx)
	if err != nil {
		// A failed publish on an ordering key pauses that key: the client
		// refuses every later message for it until ResumePublish is called, so
		// that a message cannot jump ahead of a predecessor that never landed.
		// That guard is for the client's asynchronous API, where later messages
		// are already queued when the failure arrives. Publish here is
		// synchronous and hands the error straight back, so nothing is queued
		// behind this message and the caller is the one deciding what happens
		// next — leaving the key paused would turn one transient failure into a
		// permanently dead key that no error message explains.
		if o.OrderingKey != "" {
			p.publisher.ResumePublish(o.OrderingKey)
		}

		p.instruments.Failed(ctx)

		return op.Error(err, "publishing pubsub message")
	}

	op.SpanOnly("message_id", serverID)

	p.instruments.Published(ctx, startTime)

	op.Logger().Debug("published message")

	return nil
}

func (p *pubSubPublisher) PublishAsync(ctx context.Context, data any, opts ...messagequeue.PublishOption) {
	if err := p.Publish(ctx, data, opts...); err != nil {
		p.o11y.Logger().Error("publishing message", err)
	}
}
