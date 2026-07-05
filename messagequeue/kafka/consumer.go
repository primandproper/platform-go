package kafka

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v4/messagequeue"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/keys"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/segmentio/kafka-go"
)

type (
	kafkaReader interface {
		FetchMessage(ctx context.Context) (kafka.Message, error)
		CommitMessages(ctx context.Context, msgs ...kafka.Message) error
		Close() error
	}

	kafkaConsumer struct {
		o11y            observability.Observer
		consumedCounter metrics.Int64Counter
		handlerFunc     func(context.Context, []byte) error
		reader          kafkaReader
	}
)

var _ messagequeue.Consumer = (*kafkaConsumer)(nil)

// fetchErrorBackoff is the pause after a failed fetch before retrying, so a
// persistent broker error doesn't hot-spin the consume loop.
const fetchErrorBackoff = 250 * time.Millisecond

// sendErr delivers err on errs without wedging: it also selects on ctx so a
// consumer whose error channel is no longer being drained still unblocks when the
// context is canceled during shutdown.
func (c *kafkaConsumer) sendErr(ctx context.Context, errs chan error, err error) {
	if errs == nil {
		return
	}

	// Prefer delivering the error whenever the channel can accept it right now — a
	// handler that cancels ctx and then returns an error would otherwise race the
	// send against ctx.Done() (both ready) and randomly drop the error.
	select {
	case errs <- err:
		return
	default:
	}

	select {
	case errs <- err:
	case <-ctx.Done():
	}
}

func provideKafkaConsumer(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, brokers []string, groupID, topic string, handlerFunc func(context.Context, []byte) error) (*kafkaConsumer, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	consumedCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_consumed", topic))
	if err != nil {
		return nil, fmt.Errorf("creating consumed counter: %w", err)
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: groupID,
		Topic:   topic,
	})

	return &kafkaConsumer{
		handlerFunc:     handlerFunc,
		reader:          reader,
		o11y:            observability.NewObserver(fmt.Sprintf("%s_consumer", topic), logger, tracerProvider),
		consumedCounter: consumedCounter,
	}, nil
}

// Consume reads messages from Kafka and applies the handler to their payloads.
func (c *kafkaConsumer) Consume(ctx context.Context, stopChan chan bool, errs chan error) {
	if stopChan == nil {
		stopChan = make(chan bool, 1)
	}

	// Cancel the fetch context when stop is signaled so a FetchMessage blocked
	// waiting for a message returns promptly instead of ignoring stop until the next
	// message arrives. The watcher also exits on ctx.Done (fired by defer cancel), so
	// it doesn't leak on the normal shutdown path.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-stopChan:
			cancel()
		case <-ctx.Done():
		}
	}()

	// The reader owns network connections and consumer-group membership; close it on
	// exit so neither leaks.
	defer func() {
		if err := c.reader.Close(); err != nil {
			c.o11y.Logger().Error("closing kafka reader", err)
		}
	}()

	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.sendErr(ctx, errs, err)
			// Back off before refetching so a persistent fetch error doesn't hot-spin.
			select {
			case <-ctx.Done():
				return
			case <-time.After(fetchErrorBackoff):
			}
			continue
		}

		msgCtx, op := c.o11y.BeginCustom(ctx, "consume_message")
		op.Set(keys.TopicKey, msg.Topic).Set(keys.LengthKey, len(msg.Value))
		op.SpanOnly("partition", msg.Partition).SpanOnly("offset", msg.Offset)
		c.consumedCounter.Add(msgCtx, 1)

		if err = c.handlerFunc(msgCtx, msg.Value); err != nil {
			op.Acknowledge(err, "handling message")
			c.sendErr(msgCtx, errs, err)
			// Kafka commits are cumulative by offset, so committing a later message
			// would advance the group past this failed one and lose it. Stop instead,
			// leaving the offset uncommitted for redelivery on restart/rebalance.
			op.End()
			return
		}

		if err = c.reader.CommitMessages(msgCtx, msg); err != nil {
			op.Acknowledge(err, "committing message")
			c.sendErr(msgCtx, errs, err)
		}

		op.End()
	}
}

type consumerProvider struct {
	logger          logging.Logger
	tracerProvider  tracing.TracerProvider
	metricsProvider metrics.Provider
	consumerCache   map[string]messagequeue.Consumer
	groupID         string
	brokers         []string
	consumerCacheMu sync.RWMutex
}

var _ messagequeue.ConsumerProvider = (*consumerProvider)(nil)

// ProvideKafkaConsumerProvider returns a ConsumerProvider backed by Kafka.
func ProvideKafkaConsumerProvider(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, cfg Config) messagequeue.ConsumerProvider {
	logger.WithValue("brokers", cfg.Brokers).WithValue("group_id", cfg.GroupID).Info("setting up kafka consumer")

	return &consumerProvider{
		logger:          logging.EnsureLogger(logger),
		tracerProvider:  tracerProvider,
		metricsProvider: metricsProvider,
		brokers:         cfg.Brokers,
		groupID:         cfg.GroupID,
		consumerCache:   map[string]messagequeue.Consumer{},
	}
}

// Close is a no-op: each cached consumer owns its Kafka reader and closes it when
// its Consume loop exits, so the provider holds no independent resource to release.
func (p *consumerProvider) Close() {}

// ProvideConsumer returns a Consumer for the given topic.
func (p *consumerProvider) ProvideConsumer(_ context.Context, topic string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	if topic == "" {
		return nil, messagequeue.ErrEmptyTopicName
	}

	p.consumerCacheMu.Lock()
	defer p.consumerCacheMu.Unlock()
	if cached, ok := p.consumerCache[topic]; ok {
		return cached, nil
	}

	c, err := provideKafkaConsumer(p.logger, p.tracerProvider, p.metricsProvider, p.brokers, p.groupID, topic, handlerFunc)
	if err != nil {
		return nil, err
	}

	p.consumerCache[topic] = c

	return c, nil
}
