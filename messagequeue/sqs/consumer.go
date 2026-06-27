package sqs

import (
	"context"
	"fmt"
	"sync"

	"github.com/primandproper/platform-go/errors"
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

const (
	longPollWaitSeconds = 20
	maxNumberOfMessages = 10
)

type (
	messageReceiver interface {
		ReceiveMessage(ctx context.Context, input *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
		DeleteMessage(ctx context.Context, input *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	}

	sqsConsumer struct {
		o11y            observability.Observer
		consumedCounter metrics.Int64Counter
		receiver        messageReceiver
		handlerFunc     func(context.Context, []byte) error
		queueURL        string
	}
)

func provideSQSConsumer(
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
	receiver messageReceiver,
	queueURL string,
	handlerFunc func(context.Context, []byte) error,
) *sqsConsumer {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	consumedCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_consumed", queueURL))
	if err != nil {
		panic(fmt.Sprintf("creating consumed counter: %v", err))
	}

	return &sqsConsumer{
		o11y:            observability.NewObserver(fmt.Sprintf("%s_consumer", queueURL), logger, tracerProvider),
		receiver:        receiver,
		queueURL:        queueURL,
		handlerFunc:     handlerFunc,
		consumedCounter: consumedCounter,
	}
}

// Consume polls the SQS queue and processes messages until stopChan is signaled.
// On handler success, the message is deleted from the queue.
// On handler failure, the message is not deleted (it returns after visibility timeout).
func (c *sqsConsumer) Consume(ctx context.Context, stopChan chan bool, errs chan error) {
	if stopChan == nil {
		stopChan = make(chan bool, 1)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		<-stopChan
		cancel()
	}()

	for ctx.Err() == nil {
		output, err := c.receiver.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(c.queueURL),
			MaxNumberOfMessages: maxNumberOfMessages,
			WaitTimeSeconds:     longPollWaitSeconds,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.o11y.Logger().Error("receiving SQS messages", err)
			if errs != nil {
				errs <- err
			}
			continue
		}

		for i := range output.Messages {
			msg := &output.Messages[i]
			if msg.Body == nil {
				continue
			}
			body := []byte(aws.ToString(msg.Body))

			msgCtx, op := c.o11y.BeginCustom(ctx, "consume_message")
			c.consumedCounter.Add(msgCtx, 1)
			if err = c.handlerFunc(msgCtx, body); err != nil {
				op.Acknowledge(err, "handling SQS message")
				if errs != nil {
					errs <- err
				}
				op.End()
				continue
			}

			if _, err = c.receiver.DeleteMessage(msgCtx, &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(c.queueURL),
				ReceiptHandle: msg.ReceiptHandle,
			}); err != nil {
				op.Acknowledge(err, "deleting SQS message")
				if errs != nil {
					errs <- err
				}
			}
			op.End()
		}
	}
}

type consumerProvider struct {
	o11y            observability.Observer
	tracerProvider  tracing.TracerProvider
	metricsProvider metrics.Provider
	consumerCache   map[string]messagequeue.Consumer
	sqsClient       messageReceiver
	consumerCacheMu sync.RWMutex
}

// ProvideSQSConsumerProvider returns a ConsumerProvider for SQS.
func ProvideSQSConsumerProvider(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, _ Config) (messagequeue.ConsumerProvider, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "loading default AWS config")
	}
	svc := sqs.NewFromConfig(cfg)

	return &consumerProvider{
		o11y:            observability.NewObserver("sqs_consumer_provider", logger, tracerProvider),
		tracerProvider:  tracerProvider,
		metricsProvider: metricsProvider,
		sqsClient:       svc,
		consumerCache:   map[string]messagequeue.Consumer{},
	}, nil
}

// ProvideConsumer returns a Consumer for the given topic (queue URL).
func (p *consumerProvider) ProvideConsumer(ctx context.Context, topic string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	_, op := p.o11y.Begin(ctx)
	defer op.End()

	if topic == "" {
		return nil, op.Error(messagequeue.ErrEmptyTopicName, "providing consumer")
	}

	op.Set(keys.TopicKey, topic)

	p.consumerCacheMu.Lock()
	defer p.consumerCacheMu.Unlock()
	if cached, ok := p.consumerCache[topic]; ok {
		return cached, nil
	}

	c := provideSQSConsumer(op.Logger(), p.tracerProvider, p.metricsProvider, p.sqsClient, topic, handlerFunc)
	p.consumerCache[topic] = c

	return c, nil
}
