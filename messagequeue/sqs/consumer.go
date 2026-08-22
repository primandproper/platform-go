package sqs

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/errors"
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
		o11y        observability.Observer
		instruments *mqmetrics.Consumer
		receiver    messageReceiver
		handlerFunc func(context.Context, []byte) error
		queueURL    string
	}
)

// instrumentName renders a queue URL as an identifier fit for an instrumentation
// scope: the last path segment, with anything outside [A-Za-z0-9_.-] replaced.
//
// The instruments themselves no longer need it — they carry constant names and
// take the queue URL as an attribute, where a colon and a slash are ordinary
// characters. This is the hazard SQS documented against itself and the other
// three brokers did not; naming the instruments once removed it for all four.
func instrumentName(queueURL string) string {
	name := queueURL
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}

	if name == "" {
		return "sqs"
	}

	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			return r
		default:
			return '_'
		}
	}, name)
}

func provideSQSConsumer(
	logger logging.Logger,
	tracerProvider tracing.Provider,
	metricsProvider metrics.Provider,
	receiver messageReceiver,
	queueURL string,
	handlerFunc func(context.Context, []byte) error,
) (*sqsConsumer, error) {
	instruments, err := mqmetrics.NewConsumer(metricsProvider, queueURL)
	if err != nil {
		return nil, err
	}

	return &sqsConsumer{
		// The queue URL, not the instrument name: the value identifies the queue
		// for a reader, while instrumentName only has to satisfy OpenTelemetry.
		o11y:        observability.NewObserverWithValues(fmt.Sprintf("%s_consumer", instrumentName(queueURL)), logger, tracerProvider, map[string]any{keys.TopicKey: queueURL}),
		receiver:    receiver,
		queueURL:    queueURL,
		handlerFunc: handlerFunc,
		instruments: instruments,
	}, nil
}

// Consume polls the SQS queue and processes messages until stopChan is signaled.
// On handler success, the message is deleted from the queue.
// On handler failure, the message is not deleted (it returns after visibility timeout).
func (c *sqsConsumer) Consume(ctx context.Context, errs chan<- error) {
	backoff := receivewait.New(nil, nil)

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

			c.instruments.ReceiveFailed(ctx)
			c.o11y.Logger().Error("receiving SQS messages", err)
			consumererr.Send(ctx, errs, err)

			// Long polling normally paces this loop, but a receive that fails
			// returns immediately; see the receivewait package for what a loop
			// with no wait here costs.
			if backoff.Wait(ctx) != nil {
				return
			}

			continue
		}

		backoff.Reset()

		for i := range output.Messages {
			msg := &output.Messages[i]
			if msg.Body == nil {
				continue
			}
			body := []byte(aws.ToString(msg.Body))

			msgCtx, op := c.o11y.BeginCustom(ctx, "consume_message")
			op.Set(keys.LengthKey, len(body))
			op.SpanOnly("message_id", aws.ToString(msg.MessageId))
			startedAt := time.Now()
			err = panicking.Contain(func() error { return c.handlerFunc(msgCtx, body) })
			c.instruments.Handled(msgCtx, startedAt, err)

			if err != nil {
				op.Acknowledge(err, "handling SQS message")
				consumererr.Send(msgCtx, errs, err)
				op.End()
				continue
			}

			if _, err = c.receiver.DeleteMessage(msgCtx, &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(c.queueURL),
				ReceiptHandle: msg.ReceiptHandle,
			}); err != nil {
				op.Acknowledge(err, "deleting SQS message")
				consumererr.Send(msgCtx, errs, err)
			}
			op.End()
		}
	}
}

var _ messagequeue.ConsumerProvider = (*ConsumerProvider)(nil)

// ConsumerProvider is the Amazon SQS messagequeue.ConsumerProvider implementation. It is
// exported, and returned by NewSQSConsumerProvider, so a caller who has chosen
// Amazon SQS can depend on that choice rather than on the interface every
// broker shares.
type ConsumerProvider struct {
	o11y            observability.Observer
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	consumerCache   map[string]messagequeue.Consumer
	sqsClient       messageReceiver
	consumerCacheMu sync.RWMutex
}

// NewSQSConsumerProvider returns a ConsumerProvider for SQS.
func NewSQSConsumerProvider(ctx context.Context, queueCfg Config, opts ...Option) (*ConsumerProvider, error) {
	o := newOptions(opts)

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

	return &ConsumerProvider{
		o11y:            observability.NewObserver("sqs_consumer_provider", o.logger, o.tracerProvider),
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
		sqsClient:       svc,
		consumerCache:   map[string]messagequeue.Consumer{},
	}, nil
}

// Close is a no-op: the SQS client is a stateless HTTP client with nothing to
// release.
func (p *ConsumerProvider) Close() {}

// NewConsumer returns a Consumer for the given topic (queue URL).
func (p *ConsumerProvider) NewConsumer(ctx context.Context, topic string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	_, op := p.o11y.Begin(ctx)
	defer op.End()

	if topic == "" {
		return nil, op.Error(messagequeue.ErrEmptyTopicName, "providing consumer")
	}

	op.Set(keys.TopicKey, topic)

	p.consumerCacheMu.Lock()
	defer p.consumerCacheMu.Unlock()

	// Returning the cached consumer would hand this caller someone else's
	// handler, and their own would never see a message.
	if _, ok := p.consumerCache[topic]; ok {
		return nil, op.Error(
			errors.Wrapf(messagequeue.ErrConsumerAlreadyRegistered, "topic %q", topic),
			"providing consumer",
		)
	}

	c, err := provideSQSConsumer(op.Logger(), p.tracerProvider, p.metricsProvider, p.sqsClient, topic, handlerFunc)
	if err != nil {
		return nil, op.Error(err, "providing consumer")
	}
	p.consumerCache[topic] = c

	return c, nil
}
