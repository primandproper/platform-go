package sqs

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

type mockMessageReceiver struct {
	receiveMessageFunc func(ctx context.Context, input *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	deleteMessageFunc  func(ctx context.Context, input *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	deleteMessageCalls int
}

func (m *mockMessageReceiver) ReceiveMessage(ctx context.Context, input *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return m.receiveMessageFunc(ctx, input, optFns...)
}

func (m *mockMessageReceiver) DeleteMessage(ctx context.Context, input *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	m.deleteMessageCalls++
	return m.deleteMessageFunc(ctx, input, optFns...)
}

func Test_sqsConsumer_Consume(T *testing.T) {
	T.Parallel()

	queueURL := "https://sqs.us-east-1.amazonaws.com/123456789/test-queue"

	T.Run("successful message handling and deletion", func(t *testing.T) {
		t.Parallel()

		deleteCalled := make(chan struct{}, 1)
		var receiveCalls int
		mmr := &mockMessageReceiver{
			receiveMessageFunc: func(_ context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
				receiveCalls++
				if receiveCalls == 1 {
					test.EqOp(t, queueURL, aws.ToString(in.QueueUrl))
					test.EqOp(t, int32(maxNumberOfMessages), in.MaxNumberOfMessages)
					test.EqOp(t, int32(longPollWaitSeconds), in.WaitTimeSeconds)
					return &sqs.ReceiveMessageOutput{
						Messages: []types.Message{
							{
								Body:          aws.String("test-payload"),
								ReceiptHandle: aws.String("receipt-handle-123"),
							},
						},
					}, nil
				}
				return &sqs.ReceiveMessageOutput{Messages: []types.Message{}}, nil
			},
			deleteMessageFunc: func(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
				test.EqOp(t, queueURL, aws.ToString(in.QueueUrl))
				test.EqOp(t, "receipt-handle-123", aws.ToString(in.ReceiptHandle))
				deleteCalled <- struct{}{}
				return &sqs.DeleteMessageOutput{}, nil
			},
		}

		handlerDone := make(chan []byte, 1)
		handler := func(_ context.Context, body []byte) error {
			handlerDone <- body
			return nil
		}

		consumer, err := provideSQSConsumer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, mmr, queueURL, handler)
		must.NoError(t, err)
		errs := make(chan error, 4)

		consumeCtx, stopConsuming := context.WithCancel(t.Context())
		go consumer.Consume(consumeCtx, errs)

		receivedBody := <-handlerDone
		<-deleteCalled // wait for DeleteMessage before stopping
		stopConsuming()

		test.Eq(t, []byte("test-payload"), receivedBody)
	})

	T.Run("handler error does not delete message", func(t *testing.T) {
		t.Parallel()

		anticipatedErr := errors.New("handler failed")
		var receiveCalls int
		mmr := &mockMessageReceiver{
			receiveMessageFunc: func(_ context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
				receiveCalls++
				if receiveCalls == 1 {
					return &sqs.ReceiveMessageOutput{
						Messages: []types.Message{
							{
								Body:          aws.String("fail-payload"),
								ReceiptHandle: aws.String("receipt-handle-456"),
							},
						},
					}, nil
				}
				return &sqs.ReceiveMessageOutput{Messages: []types.Message{}}, nil
			},
			deleteMessageFunc: func(_ context.Context, _ *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
				t.Fatal("DeleteMessage should not be called when handler errors")
				return nil, nil
			},
		}

		handler := func(_ context.Context, _ []byte) error {
			return anticipatedErr
		}

		consumer, err := provideSQSConsumer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, mmr, queueURL, handler)
		must.NoError(t, err)
		errs := make(chan error, 4)

		consumeCtx, stopConsuming := context.WithCancel(t.Context())
		go consumer.Consume(consumeCtx, errs)

		receivedErr := <-errs
		test.Error(t, receivedErr)
		test.ErrorIs(t, receivedErr, anticipatedErr)

		stopConsuming()

		test.EqOp(t, 0, mmr.deleteMessageCalls)
	})
}

func TestNewSQSConsumerProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := Config{}

		actual, err := NewSQSConsumerProvider(ctx, cfg)
		test.NoError(t, err)
		test.NotNil(t, actual)
	})

	T.Run("with custom QueueAddress endpoint override", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := Config{QueueAddress: "http://localhost:4566"}

		actual, err := NewSQSConsumerProvider(ctx, cfg)
		test.NoError(t, err)
		test.NotNil(t, actual)
	})
}

func Test_consumerProvider_NewConsumer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := Config{}

		provider, err := NewSQSConsumerProvider(ctx, cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		obs := observability.NewRecordingObserver()
		provider.o11y = obs

		topic := "https://sqs.us-east-1.amazonaws.com/123/test"
		actual, err := provider.NewConsumer(ctx, topic, nil)
		test.NoError(t, err)
		test.NotNil(t, actual)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey: topic,
		})
	})

	T.Run("rejects a second consumer for the same topic", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := Config{}
		topic := "https://sqs.us-east-1.amazonaws.com/123/cached-queue"

		provider, err := NewSQSConsumerProvider(ctx, cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		actual, err := provider.NewConsumer(ctx, topic, nil)
		test.NoError(t, err)
		test.NotNil(t, actual)

		// The second caller used to get the first caller's consumer, wired to the
		// first caller's handler, and their own handler never saw a message.
		actual2, err := provider.NewConsumer(ctx, topic, nil)
		test.ErrorIs(t, err, messagequeue.ErrConsumerAlreadyRegistered)
		test.Nil(t, actual2)
	})

	T.Run("with empty topic returns error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := Config{}

		provider, err := NewSQSConsumerProvider(ctx, cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		obs := observability.NewRecordingObserver()
		provider.o11y = obs

		actual, err := provider.NewConsumer(ctx, "", nil)
		test.Error(t, err)
		test.Nil(t, actual)
		test.ErrorIs(t, err, messagequeue.ErrEmptyTopicName)

		// The failure itself must have been recorded on the operation.
		op := obs.ObservedOperationWithKeys(t)
		must.SliceLen(t, 1, op.Errors)
	})
}

func Test_provideSQSConsumer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		consumer, err := provideSQSConsumer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, nil, "https://sqs.us-east-1.amazonaws.com/123/test", nil)
		must.NoError(t, err)
		must.NotNil(t, consumer)
	})

	T.Run("returns error when NewInt64Counter fails", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricnoop.Int64Counter{}, errors.New("forced error")
			},
		}

		actual, err := provideSQSConsumer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, nil, "t", nil)
		test.Error(t, err)
		test.Nil(t, actual)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

func Test_instrumentName(T *testing.T) {
	T.Parallel()

	cases := map[string]struct{ queueURL, expected string }{
		"derives the name from the queue, not the URL": {
			queueURL: "https://sqs.us-east-1.amazonaws.com/123456789/test-queue",
			expected: "test-queue",
		},
		"a bare name is kept": {
			queueURL: "my-queue",
			expected: "my-queue",
		},
		"dots, dashes and underscores survive": {
			queueURL: "https://sqs.us-east-1.amazonaws.com/1/orders.fifo_v2-a",
			expected: "orders.fifo_v2-a",
		},
		"characters an instrument name cannot carry are replaced": {
			queueURL: "https://sqs.us-east-1.amazonaws.com/1/queue name!",
			expected: "queue_name_",
		},
		"an empty queue URL falls back": {
			queueURL: "",
			expected: "sqs",
		},
		"a trailing slash falls back rather than yielding an empty name": {
			queueURL: "https://sqs.us-east-1.amazonaws.com/123456789/",
			expected: "sqs",
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, tc.expected, instrumentName(tc.queueURL))
		})
	}
}

func Test_sqsConsumer_Consume_ReceiveFailure(T *testing.T) {
	T.Parallel()

	queueURL := "https://sqs.us-east-1.amazonaws.com/123456789/test-queue"

	T.Run("a failing receive reports the error and backs off", func(t *testing.T) {
		t.Parallel()

		anticipatedErr := errors.New("expired credentials")
		calls := make(chan struct{}, 8)

		mmr := &mockMessageReceiver{
			receiveMessageFunc: func(_ context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
				select {
				case calls <- struct{}{}:
				default:
				}

				return nil, anticipatedErr
			},
		}

		consumer, err := provideSQSConsumer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, mmr,
			queueURL, func(context.Context, []byte) error { return nil })
		must.NoError(t, err)

		errs := make(chan error, 4)
		consumeCtx, stopConsuming := context.WithCancel(t.Context())
		t.Cleanup(stopConsuming)

		go consumer.Consume(consumeCtx, errs)

		receivedErr := <-errs
		test.ErrorIs(t, receivedErr, anticipatedErr)

		// Without the backoff this loop spins as fast as the CPU allows. One
		// retry inside the first backoff window is enough to show it retries at
		// all; the pacing itself is what the sleep between them provides.
		<-calls
		stopConsuming()
	})

	T.Run("a cancelled context stops the loop without reporting", func(t *testing.T) {
		t.Parallel()

		started := make(chan struct{})
		var once sync.Once

		mmr := &mockMessageReceiver{
			receiveMessageFunc: func(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
				once.Do(func() { close(started) })
				<-ctx.Done()

				// A receive that fails because the consumer is shutting down is
				// not a failure worth reporting to the caller.
				return nil, ctx.Err()
			},
		}

		consumer, err := provideSQSConsumer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, mmr,
			queueURL, func(context.Context, []byte) error { return nil })
		must.NoError(t, err)

		errs := make(chan error, 4)
		consumeCtx, stopConsuming := context.WithCancel(t.Context())

		done := make(chan struct{})
		go func() {
			consumer.Consume(consumeCtx, errs)
			close(done)
		}()

		<-started
		stopConsuming()
		<-done

		test.EqOp(t, 0, len(errs))
	})
}
