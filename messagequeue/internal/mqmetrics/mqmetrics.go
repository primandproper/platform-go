// Package mqmetrics holds the instruments every messagequeue broker records, so
// that the four brokers agree on what each number means.
package mqmetrics

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The instrument names, one set shared by every broker.
//
// These used to be built per topic — fmt.Sprintf("%s_consumed", topic) — which
// cost two things. A dashboard had no single series to aggregate over, because
// every topic was its own instrument rather than one instrument with a topic
// attribute. And the instrument name became a function of runtime config:
// OpenTelemetry accepts [A-Za-z][A-Za-z0-9_./-]{0,254} for an instrument name,
// which a PubSub subscription path ("projects/p/subscriptions/s") satisfies only
// by accident and an SQS queue URL does not satisfy at all, so a queue name that
// the broker was perfectly happy with failed instrument construction at startup.
// SQS documented that hazard against itself; it applied to all four.
const (
	MessagesPublished = "messagequeue.messages_published"
	PublishErrors     = "messagequeue.publish_errors"
	PublishLatencyMS  = "messagequeue.publish_latency_ms"
	MessagesConsumed  = "messagequeue.messages_consumed"
	HandlerErrors     = "messagequeue.handler_errors"
	ConsumeLatencyMS  = "messagequeue.consume_latency_ms"
	ReceiveErrors     = "messagequeue.receive_errors"
)

// topicAttr builds the measurement option carrying the topic every instrument
// here is recorded with.
func topicAttr(topic string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(keys.TopicKey, topic))
}

// Publisher is the instrument set a broker's publisher records.
type Publisher struct {
	published metrics.Int64Counter
	errors    metrics.Int64Counter
	latency   metrics.Float64Histogram
	attrs     metric.MeasurementOption
}

// NewPublisher builds the publisher instrument set for a topic.
func NewPublisher(metricsProvider metrics.Provider, topic string) (*Publisher, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	published, err := mp.NewInt64Counter(MessagesPublished)
	if err != nil {
		return nil, errors.Wrap(err, "creating published counter")
	}

	errCounter, err := mp.NewInt64Counter(PublishErrors)
	if err != nil {
		return nil, errors.Wrap(err, "creating publish error counter")
	}

	latency, err := mp.NewFloat64Histogram(PublishLatencyMS)
	if err != nil {
		return nil, errors.Wrap(err, "creating publish latency histogram")
	}

	return &Publisher{
		published: published,
		errors:    errCounter,
		latency:   latency,
		attrs:     topicAttr(topic),
	}, nil
}

// Failed counts a publish that did not happen.
func (p *Publisher) Failed(ctx context.Context) {
	p.errors.Add(ctx, 1, p.attrs)
}

// Published counts a publish that did, and records how long it took.
func (p *Publisher) Published(ctx context.Context, startedAt time.Time) {
	p.published.Add(ctx, 1, p.attrs)
	p.latency.Record(ctx, float64(time.Since(startedAt).Milliseconds()), p.attrs)
}

// Consumer is the instrument set a broker's consumer records.
type Consumer struct {
	consumed      metrics.Int64Counter
	handlerErrors metrics.Int64Counter
	receiveErrors metrics.Int64Counter
	latency       metrics.Float64Histogram
	attrs         metric.MeasurementOption
}

// NewConsumer builds the consumer instrument set for a topic.
func NewConsumer(metricsProvider metrics.Provider, topic string) (*Consumer, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	consumed, err := mp.NewInt64Counter(MessagesConsumed)
	if err != nil {
		return nil, errors.Wrap(err, "creating consumed counter")
	}

	handlerErrors, err := mp.NewInt64Counter(HandlerErrors)
	if err != nil {
		return nil, errors.Wrap(err, "creating handler error counter")
	}

	receiveErrors, err := mp.NewInt64Counter(ReceiveErrors)
	if err != nil {
		return nil, errors.Wrap(err, "creating receive error counter")
	}

	latency, err := mp.NewFloat64Histogram(ConsumeLatencyMS)
	if err != nil {
		return nil, errors.Wrap(err, "creating consume latency histogram")
	}

	return &Consumer{
		consumed:      consumed,
		handlerErrors: handlerErrors,
		receiveErrors: receiveErrors,
		latency:       latency,
		attrs:         topicAttr(topic),
	}, nil
}

// Handled records the outcome of one handler invocation: latency either way,
// then one of the two counters.
//
// MessagesConsumed counts here rather than on arrival. Incrementing it before
// calling the handler made it a count of messages received, which is a number the
// broker already reports and which cannot go down when the handler starts
// failing — so a consumer whose handler errored on every message showed a healthy
// climbing "consumed" line with nothing beside it to contradict that.
func (c *Consumer) Handled(ctx context.Context, startedAt time.Time, err error) {
	c.latency.Record(ctx, float64(time.Since(startedAt).Milliseconds()), c.attrs)

	if err != nil {
		c.handlerErrors.Add(ctx, 1, c.attrs)
		return
	}

	c.consumed.Add(ctx, 1, c.attrs)
}

// ReceiveFailed counts a failure to read from the broker, as distinct from a
// handler that was given a message and failed on it.
func (c *Consumer) ReceiveFailed(ctx context.Context) {
	c.receiveErrors.Add(ctx, 1, c.attrs)
}
