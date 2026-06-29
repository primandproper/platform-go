package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v2/messagequeue"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v2/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

type mockMessagePublisher struct {
	sendMessageFunc  func(ctx context.Context, input *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	sendMessageCalls int
}

func (m *mockMessagePublisher) SendMessage(ctx context.Context, input *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	m.sendMessageCalls++
	return m.sendMessageFunc(ctx, input, optFns...)
}

func Test_sqsPublisher_Publish(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		provider := ProvideSQSPublisherProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil)
		must.NotNil(t, provider)

		a, err := provider.ProvidePublisher(ctx, t.Name())
		test.NotNil(t, a)
		test.NoError(t, err)

		actual, ok := a.(*sqsPublisher)
		must.True(t, ok)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		mmp := &mockMessagePublisher{
			sendMessageFunc: func(_ context.Context, _ *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
				return &sqs.SendMessageOutput{}, nil
			},
		}

		actual.publisher = mmp

		obs := observability.NewRecordingObserver()
		actual.o11y = obs

		err = actual.Publish(ctx, inputData)
		test.NoError(t, err)
		test.EqOp(t, 1, mmp.sendMessageCalls)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey: actual.topic,
		})
	})

	T.Run("with error encoding value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		provider := ProvideSQSPublisherProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil)
		must.NotNil(t, provider)

		a, err := provider.ProvidePublisher(ctx, t.Name())
		test.NotNil(t, a)
		test.NoError(t, err)

		actual, ok := a.(*sqsPublisher)
		must.True(t, ok)

		obs := observability.NewRecordingObserver()
		actual.o11y = obs

		inputData := &struct {
			Name json.Number `json:"name"`
		}{
			Name: json.Number(t.Name()),
		}

		err = actual.Publish(ctx, inputData)
		test.Error(t, err)

		// Even though publishing failed, the topic must still have been observed.
		obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey: actual.topic,
		})
	})
}

func Test_sqsPublisher_PublishAsync(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		provider := ProvideSQSPublisherProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil)
		must.NotNil(t, provider)

		a, err := provider.ProvidePublisher(ctx, t.Name())
		test.NotNil(t, a)
		test.NoError(t, err)

		actual, ok := a.(*sqsPublisher)
		must.True(t, ok)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		mmp := &mockMessagePublisher{
			sendMessageFunc: func(_ context.Context, _ *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
				return &sqs.SendMessageOutput{}, nil
			},
		}

		actual.publisher = mmp

		actual.PublishAsync(ctx, inputData)
		test.EqOp(t, 1, mmp.sendMessageCalls)
	})

	T.Run("with error encoding value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		provider := ProvideSQSPublisherProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil)
		must.NotNil(t, provider)

		a, err := provider.ProvidePublisher(ctx, t.Name())
		test.NotNil(t, a)
		test.NoError(t, err)

		actual, ok := a.(*sqsPublisher)
		must.True(t, ok)

		inputData := &struct {
			Name json.Number `json:"name"`
		}{
			Name: json.Number(t.Name()),
		}

		actual.PublishAsync(ctx, inputData)
	})

	T.Run("with SendMessage error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		provider := ProvideSQSPublisherProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil)
		must.NotNil(t, provider)

		a, err := provider.ProvidePublisher(ctx, t.Name())
		test.NotNil(t, a)
		test.NoError(t, err)

		actual, ok := a.(*sqsPublisher)
		must.True(t, ok)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		mmp := &mockMessagePublisher{
			sendMessageFunc: func(_ context.Context, _ *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
				return nil, errors.New("send failed")
			},
		}

		actual.publisher = mmp

		actual.PublishAsync(ctx, inputData)
		test.EqOp(t, 1, mmp.sendMessageCalls)
	})
}

func TestProvideSQSPublisherProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		actual := ProvideSQSPublisherProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil)
		test.NotNil(t, actual)
	})
}

func Test_publisherProvider_ProvidePublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		provider := ProvideSQSPublisherProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil)
		must.NotNil(t, provider)

		actual, err := provider.ProvidePublisher(ctx, t.Name())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with cache hit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		provider := ProvideSQSPublisherProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil)
		must.NotNil(t, provider)

		actual, err := provider.ProvidePublisher(ctx, t.Name())
		test.NotNil(t, actual)
		test.NoError(t, err)

		actual, err = provider.ProvidePublisher(ctx, t.Name())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with empty topic", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		provider := ProvideSQSPublisherProvider(ctx, logger, tracingnoop.NewTracerProvider(), nil)
		must.NotNil(t, provider)

		actual, err := provider.ProvidePublisher(ctx, "")
		test.Nil(t, actual)
		test.ErrorIs(t, err, messagequeue.ErrEmptyTopicName)
	})
}

func Test_provideSQSPublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		publisher := provideSQSPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), nil, "test-topic")
		must.NotNil(t, publisher)
	})

	T.Run("panics when first NewInt64Counter fails", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				if name == "t_published" {
					return metricnoop.Int64Counter{}, errors.New("forced error")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", name)
				return nil, nil
			},
		}

		test.Panic(t, func() {
			provideSQSPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
		})
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("panics when second NewInt64Counter fails", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch name {
				case "t_published":
					return metricnoop.Int64Counter{}, nil
				case "t_publish_errors":
					return metricnoop.Int64Counter{}, errors.New("forced error")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", name)
				return nil, nil
			},
		}

		test.Panic(t, func() {
			provideSQSPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
		})
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("panics when NewFloat64Histogram fails", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricnoop.Int64Counter{}, nil
			},
			NewFloat64HistogramFunc: func(string, ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				return metricnoop.Float64Histogram{}, errors.New("forced error")
			},
		}

		test.Panic(t, func() {
			provideSQSPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
		})
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}
