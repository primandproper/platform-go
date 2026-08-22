package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/mqmetrics"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

type mockMessagePublisher struct {
	sendMessageFunc  func(ctx context.Context, input *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	sent             []*sqs.SendMessageInput
	sendMessageCalls int
}

func (m *mockMessagePublisher) SendMessage(ctx context.Context, input *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	m.sendMessageCalls++
	m.sent = append(m.sent, input)
	return m.sendMessageFunc(ctx, input, optFns...)
}

// buildPublisherWithMock returns a publisher whose SQS client is a mock that
// succeeds and records what it was handed.
func buildPublisherWithMock(t *testing.T) (*sqsPublisher, *mockMessagePublisher) {
	t.Helper()

	ctx := t.Context()

	provider, err := NewSQSPublisherProvider(ctx, Config{})
	must.NoError(t, err)

	a, err := provider.NewPublisher(ctx, t.Name())
	must.NoError(t, err)

	pub, ok := a.(*sqsPublisher)
	must.True(t, ok)

	mmp := &mockMessagePublisher{
		sendMessageFunc: func(_ context.Context, _ *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
			return &sqs.SendMessageOutput{}, nil
		},
	}
	pub.publisher = mmp

	return pub, mmp
}

func Test_sqsPublisher_Publish_fifoFields(T *testing.T) {
	T.Parallel()

	T.Run("with an ordering key", func(t *testing.T) {
		t.Parallel()

		pub, mmp := buildPublisherWithMock(t)

		must.NoError(t, pub.Publish(t.Context(), map[string]string{"name": t.Name()}, messagequeue.WithOrderingKey("account_123")))

		must.SliceLen(t, 1, mmp.sent)
		must.NotNil(t, mmp.sent[0].MessageGroupId)
		test.EqOp(t, "account_123", *mmp.sent[0].MessageGroupId)
		test.Nil(t, mmp.sent[0].MessageDeduplicationId)
	})

	T.Run("with a deduplication key", func(t *testing.T) {
		t.Parallel()

		pub, mmp := buildPublisherWithMock(t)

		must.NoError(t, pub.Publish(t.Context(), map[string]string{"name": t.Name()}, messagequeue.WithDeduplicationKey("event_456")))

		must.SliceLen(t, 1, mmp.sent)
		must.NotNil(t, mmp.sent[0].MessageDeduplicationId)
		test.EqOp(t, "event_456", *mmp.sent[0].MessageDeduplicationId)
		test.Nil(t, mmp.sent[0].MessageGroupId)
	})

	T.Run("with both, as a FIFO queue without content-based deduplication needs", func(t *testing.T) {
		t.Parallel()

		pub, mmp := buildPublisherWithMock(t)

		must.NoError(t, pub.Publish(
			t.Context(),
			map[string]string{"name": t.Name()},
			messagequeue.WithOrderingKey("account_123"),
			messagequeue.WithDeduplicationKey("event_456"),
		))

		must.SliceLen(t, 1, mmp.sent)
		must.NotNil(t, mmp.sent[0].MessageGroupId)
		must.NotNil(t, mmp.sent[0].MessageDeduplicationId)
		test.EqOp(t, "account_123", *mmp.sent[0].MessageGroupId)
		test.EqOp(t, "event_456", *mmp.sent[0].MessageDeduplicationId)
	})

	T.Run("with neither", func(t *testing.T) {
		t.Parallel()

		pub, mmp := buildPublisherWithMock(t)

		must.NoError(t, pub.Publish(t.Context(), map[string]string{"name": t.Name()}))

		// Absent has to be nil, not a pointer to "": the SDK omits the first
		// from the request and sends the second.
		must.SliceLen(t, 1, mmp.sent)
		test.Nil(t, mmp.sent[0].MessageGroupId)
		test.Nil(t, mmp.sent[0].MessageDeduplicationId)
	})

	T.Run("with empty keys", func(t *testing.T) {
		t.Parallel()

		pub, mmp := buildPublisherWithMock(t)

		must.NoError(t, pub.Publish(
			t.Context(),
			map[string]string{"name": t.Name()},
			messagequeue.WithOrderingKey(""),
			messagequeue.WithDeduplicationKey(""),
		))

		must.SliceLen(t, 1, mmp.sent)
		test.Nil(t, mmp.sent[0].MessageGroupId)
		test.Nil(t, mmp.sent[0].MessageDeduplicationId)
	})

	T.Run("PublishAsync forwards the options", func(t *testing.T) {
		t.Parallel()

		pub, mmp := buildPublisherWithMock(t)

		pub.PublishAsync(t.Context(), map[string]string{"name": t.Name()}, messagequeue.WithOrderingKey("account_123"))

		must.SliceLen(t, 1, mmp.sent)
		must.NotNil(t, mmp.sent[0].MessageGroupId)
		test.EqOp(t, "account_123", *mmp.sent[0].MessageGroupId)
	})
}

func Test_sqsPublisher_Publish(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		provider, provErr := NewSQSPublisherProvider(ctx, Config{})
		must.NoError(t, provErr)
		must.NotNil(t, provider)

		a, err := provider.NewPublisher(ctx, t.Name())
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

		// Seeded as provideSQSPublisher seeds it: the topic is stated once at
		// construction, not at each Publish.
		obs := observability.NewRecordingObserverWithValues(map[string]any{keys.TopicKey: actual.topic})
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

		provider, provErr := NewSQSPublisherProvider(ctx, Config{})
		must.NoError(t, provErr)
		must.NotNil(t, provider)

		a, err := provider.NewPublisher(ctx, t.Name())
		test.NotNil(t, a)
		test.NoError(t, err)

		actual, ok := a.(*sqsPublisher)
		must.True(t, ok)

		// Seeded as provideSQSPublisher seeds it: the topic is stated once at
		// construction, not at each Publish.
		obs := observability.NewRecordingObserverWithValues(map[string]any{keys.TopicKey: actual.topic})
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

		provider, provErr := NewSQSPublisherProvider(ctx, Config{})
		must.NoError(t, provErr)
		must.NotNil(t, provider)

		a, err := provider.NewPublisher(ctx, t.Name())
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

		provider, provErr := NewSQSPublisherProvider(ctx, Config{})
		must.NoError(t, provErr)
		must.NotNil(t, provider)

		a, err := provider.NewPublisher(ctx, t.Name())
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

		provider, provErr := NewSQSPublisherProvider(ctx, Config{})
		must.NoError(t, provErr)
		must.NotNil(t, provider)

		a, err := provider.NewPublisher(ctx, t.Name())
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

func TestNewSQSPublisherProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		actual, provErr := NewSQSPublisherProvider(ctx, Config{})
		must.NoError(t, provErr)
		test.NotNil(t, actual)
	})

	T.Run("with custom QueueAddress endpoint override", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		actual, provErr := NewSQSPublisherProvider(ctx, Config{QueueAddress: "http://localhost:4566"})
		must.NoError(t, provErr)
		test.NotNil(t, actual)
	})
}

func Test_publisherProvider_NewPublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		provider, provErr := NewSQSPublisherProvider(ctx, Config{})
		must.NoError(t, provErr)
		must.NotNil(t, provider)

		actual, err := provider.NewPublisher(ctx, t.Name())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with cache hit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		provider, provErr := NewSQSPublisherProvider(ctx, Config{})
		must.NoError(t, provErr)
		must.NotNil(t, provider)

		actual, err := provider.NewPublisher(ctx, t.Name())
		test.NotNil(t, actual)
		test.NoError(t, err)

		actual, err = provider.NewPublisher(ctx, t.Name())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with empty topic", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		provider, provErr := NewSQSPublisherProvider(ctx, Config{})
		must.NoError(t, provErr)
		must.NotNil(t, provider)

		actual, err := provider.NewPublisher(ctx, "")
		test.Nil(t, actual)
		test.ErrorIs(t, err, messagequeue.ErrEmptyTopicName)
	})
}

func Test_provideSQSPublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		publisher, err := provideSQSPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), nil, "test-topic")
		must.NoError(t, err)
		must.NotNil(t, publisher)
	})

	T.Run("returns error when first NewInt64Counter fails", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				if name == mqmetrics.MessagesPublished {
					return metricnoop.Int64Counter{}, errors.New("forced error")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", name)
				return nil, nil
			},
		}

		actual, err := provideSQSPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
		test.Error(t, err)
		test.Nil(t, actual)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("returns error when second NewInt64Counter fails", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch name {
				case mqmetrics.MessagesPublished:
					return metricnoop.Int64Counter{}, nil
				case mqmetrics.PublishErrors:
					return metricnoop.Int64Counter{}, errors.New("forced error")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", name)
				return nil, nil
			},
		}

		actual, err := provideSQSPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
		test.Error(t, err)
		test.Nil(t, actual)
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("returns error when NewFloat64Histogram fails", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricnoop.Int64Counter{}, nil
			},
			NewFloat64HistogramFunc: func(string, ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				return metricnoop.Float64Histogram{}, errors.New("forced error")
			},
		}

		actual, err := provideSQSPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
		test.Error(t, err)
		test.Nil(t, actual)
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}
