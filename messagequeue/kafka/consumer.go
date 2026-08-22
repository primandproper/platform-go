package kafka

import (
	"context"
	"fmt"
	"sync"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/consumererr"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/mqmetrics"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/receivewait"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/panicking"

	"github.com/segmentio/kafka-go"
)

type (
	kafkaReader interface {
		FetchMessage(ctx context.Context) (kafka.Message, error)
		CommitMessages(ctx context.Context, msgs ...kafka.Message) error
		Close() error
	}

	kafkaConsumer struct {
		o11y        observability.Observer
		instruments *mqmetrics.Consumer
		handlerFunc func(context.Context, []byte) error
		reader      kafkaReader
	}
)

var _ messagequeue.Consumer = (*kafkaConsumer)(nil)

func provideKafkaConsumer(logger logging.Logger, tracerProvider tracing.Provider, metricsProvider metrics.Provider, brokers []string, groupID, topic string, handlerFunc func(context.Context, []byte) error) (*kafkaConsumer, error) {
	instruments, err := mqmetrics.NewConsumer(metricsProvider, topic)
	if err != nil {
		return nil, err
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: groupID,
		Topic:   topic,
	})

	return &kafkaConsumer{
		handlerFunc: handlerFunc,
		reader:      reader,
		o11y:        observability.NewObserverWithValues(fmt.Sprintf("%s_consumer", topic), logger, tracerProvider, map[string]any{keys.TopicKey: topic}),
		instruments: instruments,
	}, nil
}

// Consume reads messages from Kafka and applies the handler to their payloads.
func (c *kafkaConsumer) Consume(ctx context.Context, errs chan<- error) {
	// The reader owns network connections and consumer-group membership; close it on
	// exit so neither leaks.
	defer func() {
		if err := c.reader.Close(); err != nil {
			c.o11y.Logger().Error("closing kafka reader", err)
		}
	}()

	backoff := receivewait.New(nil, nil)

	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Sent to errs but, until now, never logged — so a consumer failing
			// every fetch was silent in the logs unless whoever owned errs happened
			// to log what arrived on it.
			c.instruments.ReceiveFailed(ctx)
			c.o11y.Logger().Error("fetching kafka message", err)
			consumererr.Send(ctx, errs, err)
			// Back off before refetching; see the receivewait package for what a
			// loop with no wait here costs, and why the schedule is shared with
			// every other consumer rather than chosen per broker.
			if backoff.Wait(ctx) != nil {
				return
			}

			continue
		}

		msgCtx, op := c.o11y.BeginCustom(ctx, "consume_message")
		op.Set(keys.LengthKey, len(msg.Value))
		op.SpanOnly("partition", msg.Partition).SpanOnly("offset", msg.Offset)

		startedAt := time.Now()
		err = panicking.Contain(func() error { return c.handlerFunc(msgCtx, msg.Value) })
		c.instruments.Handled(msgCtx, startedAt, err)

		if err != nil {
			op.Acknowledge(err, "handling message")
			consumererr.Send(msgCtx, errs, err)
			// Kafka commits are cumulative by offset, so committing a later message
			// would advance the group past this failed one and lose it. Stop instead,
			// leaving the offset uncommitted for redelivery on restart/rebalance.
			op.End()
			return
		}

		if err = c.reader.CommitMessages(msgCtx, msg); err != nil {
			op.Acknowledge(err, "committing message")
			consumererr.Send(msgCtx, errs, err)
		}

		op.End()
	}
}

// ConsumerProvider is the Kafka messagequeue.ConsumerProvider implementation. It is
// exported, and returned by NewKafkaConsumerProvider, so a caller who has chosen
// Kafka can depend on that choice rather than on the interface every
// broker shares.
type ConsumerProvider struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	consumerCache   map[string]messagequeue.Consumer
	groupID         string
	brokers         []string
	consumerCacheMu sync.RWMutex
}

var _ messagequeue.ConsumerProvider = (*ConsumerProvider)(nil)

// NewKafkaConsumerProvider returns a ConsumerProvider backed by Kafka.
func NewKafkaConsumerProvider(cfg Config, opts ...Option) *ConsumerProvider {
	o := newOptions(opts)
	logger := logging.EnsureLogger(o.logger)
	logger.WithValue("brokers", cfg.Brokers).WithValue("group_id", cfg.GroupID).Info("setting up kafka consumer")

	return &ConsumerProvider{
		logger:          logger,
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
		brokers:         cfg.Brokers,
		groupID:         cfg.GroupID,
		consumerCache:   map[string]messagequeue.Consumer{},
	}
}

// Close is a no-op: each cached consumer owns its Kafka reader and closes it when
// its Consume loop exits, so the provider holds no independent resource to release.
func (p *ConsumerProvider) Close() {}

// NewConsumer returns a Consumer for the given topic.
func (p *ConsumerProvider) NewConsumer(_ context.Context, topic string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	if topic == "" {
		return nil, messagequeue.ErrEmptyTopicName
	}

	p.consumerCacheMu.Lock()
	defer p.consumerCacheMu.Unlock()

	// Returning the cached consumer would hand this caller someone else's
	// handler — and, once a handler error has stopped that consumer's read loop,
	// a consumer that is permanently dead but still cached.
	if _, ok := p.consumerCache[topic]; ok {
		return nil, platformerrors.Wrapf(messagequeue.ErrConsumerAlreadyRegistered, "topic %q", topic)
	}

	c, err := provideKafkaConsumer(p.logger, p.tracerProvider, p.metricsProvider, p.brokers, p.groupID, topic, handlerFunc)
	if err != nil {
		return nil, err
	}

	p.consumerCache[topic] = c

	return c, nil
}
