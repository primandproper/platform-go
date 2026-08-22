package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/mqmetrics"
	"github.com/primandproper/platform-go/v13/observability"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/redis/go-redis/v9"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

type mockMessagePublisher struct {
	publishFunc func(ctx context.Context, channel string, message any) *redis.IntCmd
	closeFunc   func() error
	pingFunc    func(ctx context.Context) *redis.StatusCmd
	publishArgs []publishCall
}

type publishCall struct {
	ctx     context.Context
	message any
	channel string
}

func (m *mockMessagePublisher) Publish(ctx context.Context, channel string, message any) *redis.IntCmd {
	m.publishArgs = append(m.publishArgs, publishCall{ctx: ctx, channel: channel, message: message})
	return m.publishFunc(ctx, channel, message)
}

func (m *mockMessagePublisher) Close() error {
	return m.closeFunc()
}

func (m *mockMessagePublisher) Ping(ctx context.Context) *redis.StatusCmd {
	return m.pingFunc(ctx)
}

func buildRedisBackedPublisher(t *testing.T, cfg *Config, topic string) messagequeue.Publisher {
	t.Helper()

	ctx := t.Context()
	provider, err := NewRedisPublisherProvider(t.Context(), *cfg)
	must.NoError(t, err)

	publisher, err := provider.NewPublisher(ctx, topic)
	must.NoError(t, err)

	return publisher
}

func Test_redisPublisher_Publish(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			QueueAddresses: []string{t.Name()},
		}
		provider, err := NewRedisPublisherProvider(t.Context(), cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		a, err := provider.NewPublisher(ctx, t.Name())
		test.NotNil(t, a)
		test.NoError(t, err)

		actual, ok := a.(*redisPublisher)
		must.True(t, ok)

		obs := observability.NewRecordingObserver()
		actual.o11y = obs

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		mmp := &mockMessagePublisher{
			publishFunc: func(_ context.Context, _ string, _ any) *redis.IntCmd { return &redis.IntCmd{} },
		}

		actual.publisher = mmp

		err = actual.Publish(ctx, inputData)
		test.NoError(t, err)

		must.SliceLen(t, 1, mmp.publishArgs)
		test.EqOp(t, actual.topic, mmp.publishArgs[0].channel)
		test.Eq(t, any(fmt.Appendf(nil, `{"name":%q}`, t.Name())), mmp.publishArgs[0].message)

		// The publish opened and ended an observed operation with no recorded error.
		op := obs.ObservedOperationWithKeys(t)
		test.SliceEmpty(t, op.Errors)
	})

	T.Run("with error encoding value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			QueueAddresses: []string{t.Name()},
		}
		provider, err := NewRedisPublisherProvider(t.Context(), cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		a, err := provider.NewPublisher(ctx, t.Name())
		test.NotNil(t, a)
		test.NoError(t, err)

		actual, ok := a.(*redisPublisher)
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

		// Even though the publish failed, the operation must have ended and the
		// failure must have been recorded on it.
		op := obs.ObservedOperationWithKeys(t)
		must.SliceLen(t, 1, op.Errors)
	})
}

func Test_redisPublisher_Stop(T *testing.T) {
	T.Parallel()

	T.Run("does not close the shared client used by other topics", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		provider, err := NewRedisPublisherProvider(t.Context(), Config{QueueAddresses: []string{t.Name()}})
		must.NoError(t, err)

		mmp := &mockMessagePublisher{
			publishFunc: func(context.Context, string, any) *redis.IntCmd { return &redis.IntCmd{} },
			closeFunc: func() error {
				t.Error("Stop must not close the shared redis client")
				return nil
			},
		}
		provider.redisClient = mmp

		pub1, err := provider.NewPublisher(ctx, "topic-1")
		must.NoError(t, err)
		pub2, err := provider.NewPublisher(ctx, "topic-2")
		must.NoError(t, err)

		pub1.Stop()

		// The second topic's publisher must still function after the first is stopped.
		test.NoError(t, pub2.Publish(ctx, map[string]string{"key": "value"}))
		must.SliceLen(t, 1, mmp.publishArgs)
	})
}

func Test_redisPublisher_Publish_ignoresPublishOptions(T *testing.T) {
	T.Parallel()

	T.Run("publishes the same message with or without options", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		provider, err := NewRedisPublisherProvider(ctx, Config{QueueAddresses: []string{t.Name()}})
		must.NoError(t, err)

		a, err := provider.NewPublisher(ctx, t.Name())
		must.NoError(t, err)

		actual, ok := a.(*redisPublisher)
		must.True(t, ok)

		mmp := &mockMessagePublisher{
			publishFunc: func(_ context.Context, _ string, _ any) *redis.IntCmd { return &redis.IntCmd{} },
		}
		actual.publisher = mmp

		inputData := map[string]string{"name": t.Name()}

		must.NoError(t, actual.Publish(ctx, inputData))
		must.NoError(t, actual.Publish(ctx, inputData,
			messagequeue.WithOrderingKey("account_123"),
			messagequeue.WithDeduplicationKey("event_456"),
		))
		actual.PublishAsync(ctx, inputData, messagequeue.WithOrderingKey("account_123"))

		// Redis pub/sub has nothing to carry either key on, so all three
		// publishes reach it as the same channel and the same bytes. The options
		// are accepted so a caller can pass one set to any backend, not because
		// this one honors them.
		must.SliceLen(t, 3, mmp.publishArgs)
		for _, call := range mmp.publishArgs {
			test.EqOp(t, actual.topic, call.channel)
			test.Eq(t, mmp.publishArgs[0].message, call.message)
		}
	})
}

func Test_redisPublisher_PublishAsync(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			QueueAddresses: []string{t.Name()},
		}
		provider, err := NewRedisPublisherProvider(t.Context(), cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		a, err := provider.NewPublisher(ctx, t.Name())
		test.NotNil(t, a)
		test.NoError(t, err)

		actual, ok := a.(*redisPublisher)
		must.True(t, ok)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		mmp := &mockMessagePublisher{
			publishFunc: func(_ context.Context, _ string, _ any) *redis.IntCmd { return &redis.IntCmd{} },
		}

		actual.publisher = mmp

		actual.PublishAsync(ctx, inputData)

		must.SliceLen(t, 1, mmp.publishArgs)
		test.EqOp(t, actual.topic, mmp.publishArgs[0].channel)
		test.Eq(t, any(fmt.Appendf(nil, `{"name":%q}`, t.Name())), mmp.publishArgs[0].message)
	})

	T.Run("with error encoding value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			QueueAddresses: []string{t.Name()},
		}
		provider, err := NewRedisPublisherProvider(t.Context(), cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		a, err := provider.NewPublisher(ctx, t.Name())
		test.NotNil(t, a)
		test.NoError(t, err)

		actual, ok := a.(*redisPublisher)
		must.True(t, ok)

		inputData := &struct {
			Name json.Number `json:"name"`
		}{
			Name: json.Number(t.Name()),
		}

		actual.PublishAsync(ctx, inputData)
	})
}

func TestNewRedisPublisherProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			QueueAddresses: []string{t.Name()},
		}
		actual, err := NewRedisPublisherProvider(t.Context(), cfg)
		must.NoError(t, err)
		test.NotNil(t, actual)
	})

	// An empty address list used to fall through both client branches and hand
	// back a provider holding a nil client, which panicked on first publish.
	T.Run("without any queue addresses", func(t *testing.T) {
		t.Parallel()

		actual, err := NewRedisPublisherProvider(t.Context(), Config{})
		test.Nil(t, actual)
		test.Error(t, err)
	})
}

func Test_publisherProvider_NewPublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			QueueAddresses: []string{t.Name()},
		}
		provider, err := NewRedisPublisherProvider(t.Context(), cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		actual, err := provider.NewPublisher(ctx, t.Name())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with cache hit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			QueueAddresses: []string{t.Name()},
		}
		provider, err := NewRedisPublisherProvider(t.Context(), cfg)
		must.NoError(t, err)
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

		cfg := Config{
			QueueAddresses: []string{t.Name()},
		}
		provider, err := NewRedisPublisherProvider(t.Context(), cfg)
		must.NoError(t, err)
		must.NotNil(t, provider)

		actual, err := provider.NewPublisher(ctx, "")
		test.Nil(t, actual)
		test.ErrorIs(t, err, messagequeue.ErrEmptyTopicName)
	})
}

func Test_provideRedisPublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		publisher, err := provideRedisPublisher(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, nil, "test-topic")
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

		actual, err := provideRedisPublisher(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, nil, "t")
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

		actual, err := provideRedisPublisher(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, nil, "t")
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

		actual, err := provideRedisPublisher(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, nil, "t")
		test.Error(t, err)
		test.Nil(t, actual)
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}
