package kafka

import (
	"context"
	"fmt"
	"sync"

	"github.com/primandproper/platform-go/messagequeue"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

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

	for {
		select {
		case <-ctx.Done():
			return
		case <-stopChan:
			return
		default:
		}

		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errs != nil {
				errs <- err
			}
			continue
		}

		msgCtx, op := c.o11y.BeginCustom(ctx, "consume_message")
		op.Set(keys.TopicKey, msg.Topic).Set(keys.LengthKey, len(msg.Value))
		op.SpanOnly("partition", msg.Partition).SpanOnly("offset", msg.Offset)
		c.consumedCounter.Add(msgCtx, 1)

		if err = c.handlerFunc(msgCtx, msg.Value); err != nil {
			op.Acknowledge(err, "handling message")
			if errs != nil {
				errs <- err
			}
		} else if err = c.reader.CommitMessages(msgCtx, msg); err != nil {
			op.Acknowledge(err, "committing message")
			if errs != nil {
				errs <- err
			}
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

// ProvideConsumer returns a Consumer for the given topic.
func (p *consumerProvider) ProvideConsumer(_ context.Context, topic string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	if topic == "" {
		return nil, ErrEmptyInputProvided
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
