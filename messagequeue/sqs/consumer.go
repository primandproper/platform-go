package sqs

import (
	"context"
	"fmt"
	"sync"

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

// sendErr delivers err on errs without wedging: it also selects on ctx so a
// consumer whose error channel is no longer being drained still unblocks when the
// context is canceled during shutdown.
func (c *sqsConsumer) sendErr(ctx context.Context, errs chan error, err error) {
	if errs == nil {
		return
	}

	// Prefer delivering the error whenever the channel can accept it right now, so a
	// canceled ctx doesn't race the send (both select cases ready) and drop the error.
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

func provideSQSConsumer(
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
	receiver messageReceiver,
	queueURL string,
	handlerFunc func(context.Context, []byte) error,
) (*sqsConsumer, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	consumedCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_consumed", queueURL))
	if err != nil {
		return nil, fmt.Errorf("creating consumed counter: %w", err)
	}

	return &sqsConsumer{
		o11y:            observability.NewObserver(fmt.Sprintf("%s_consumer", queueURL), logger, tracerProvider),
		receiver:        receiver,
		queueURL:        queueURL,
		handlerFunc:     handlerFunc,
		consumedCounter: consumedCounter,
	}, nil
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

	// Also select on ctx.Done so this watcher exits on the normal (external ctx
	// cancellation) shutdown path instead of blocking forever on <-stopChan.
	go func() {
		select {
		case <-stopChan:
			cancel()
		case <-ctx.Done():
		}
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
			c.sendErr(ctx, errs, err)
			continue
		}

		for i := range output.Messages {
			msg := &output.Messages[i]
			if msg.Body == nil {
				continue
			}
			body := []byte(aws.ToString(msg.Body))

			msgCtx, op := c.o11y.BeginCustom(ctx, "consume_message")
			op.Set(keys.TopicKey, c.queueURL).Set(keys.LengthKey, len(body))
			op.SpanOnly("message_id", aws.ToString(msg.MessageId))
			c.consumedCounter.Add(msgCtx, 1)
			if err = c.handlerFunc(msgCtx, body); err != nil {
				op.Acknowledge(err, "handling SQS message")
				c.sendErr(msgCtx, errs, err)
				op.End()
				continue
			}

			if _, err = c.receiver.DeleteMessage(msgCtx, &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(c.queueURL),
				ReceiptHandle: msg.ReceiptHandle,
			}); err != nil {
				op.Acknowledge(err, "deleting SQS message")
				c.sendErr(msgCtx, errs, err)
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
func ProvideSQSConsumerProvider(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, queueCfg Config) (messagequeue.ConsumerProvider, error) {
	var loadOpts []func(*config.LoadOptions) error
	if queueCfg.QueueAddress != "" {
		// Override the AWS endpoint (e.g. to point at localstack) when configured.
		loadOpts = append(loadOpts, config.WithBaseEndpoint(queueCfg.QueueAddress))
	}

	cfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
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

// Close is a no-op: the SQS client is a stateless HTTP client with nothing to
// release.
func (p *consumerProvider) Close() {}

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

	c, err := provideSQSConsumer(op.Logger(), p.tracerProvider, p.metricsProvider, p.sqsClient, topic, handlerFunc)
	if err != nil {
		return nil, op.Error(err, "providing consumer")
	}
	p.consumerCache[topic] = c

	return c, nil
}
