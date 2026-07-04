package sqs

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v3/messagequeue"
	"github.com/primandproper/platform-go/v3/observability"
	"github.com/primandproper/platform-go/v3/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v3/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"

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
		stopChan := make(chan bool, 1)
		errs := make(chan error, 4)

		go consumer.Consume(t.Context(), stopChan, errs)

		receivedBody := <-handlerDone
		<-deleteCalled // wait for DeleteMessage before stopping
		stopChan <- true

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
		stopChan := make(chan bool, 1)
		errs := make(chan error, 4)

		go consumer.Consume(t.Context(), stopChan, errs)

		receivedErr := <-errs
		test.Error(t, receivedErr)
		test.ErrorIs(t, receivedErr, anticipatedErr)

		stopChan <- true

		test.EqOp(t, 0, mmr.deleteMessageCalls)
	})
}

func TestProvideSQSConsumerProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := Config{}

		actual, err := ProvideSQSConsumerProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil, cfg)
		test.NoError(t, err)
		test.NotNil(t, actual)
	})

	T.Run("with custom QueueAddress endpoint override", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := Config{QueueAddress: "http://localhost:4566"}

		actual, err := ProvideSQSConsumerProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil, cfg)
		test.NoError(t, err)
		test.NotNil(t, actual)
	})
}

func Test_consumerProvider_ProvideConsumer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := Config{}

		provider, err := ProvideSQSConsumerProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil, cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		cp, ok := provider.(*consumerProvider)
		must.True(t, ok)

		obs := observability.NewRecordingObserver()
		cp.o11y = obs

		topic := "https://sqs.us-east-1.amazonaws.com/123/test"
		actual, err := provider.ProvideConsumer(ctx, topic, nil)
		test.NoError(t, err)
		test.NotNil(t, actual)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey: topic,
		})
	})

	T.Run("with cache hit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := Config{}
		topic := "https://sqs.us-east-1.amazonaws.com/123/cached-queue"

		provider, err := ProvideSQSConsumerProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil, cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		actual, err := provider.ProvideConsumer(ctx, topic, nil)
		test.NoError(t, err)
		test.NotNil(t, actual)

		actual2, err := provider.ProvideConsumer(ctx, topic, nil)
		test.NoError(t, err)
		test.NotNil(t, actual2)
		test.EqOp(t, actual, actual2)
	})

	T.Run("with empty topic returns error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := Config{}

		provider, err := ProvideSQSConsumerProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil, cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		cp, ok := provider.(*consumerProvider)
		must.True(t, ok)

		obs := observability.NewRecordingObserver()
		cp.o11y = obs

		actual, err := provider.ProvideConsumer(ctx, "", nil)
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

		mp := &mockmetrics.ProviderMock{
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
