package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/messagequeue"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

// buildRedisBackedConsumer builds a Redis container-backed messagequeue.Consumer.
func buildRedisBackedConsumer(t *testing.T, cfg *Config, topic string, handlerFunc func(context.Context, []byte) error) messagequeue.Consumer {
	t.Helper()

	provider := ProvideRedisConsumerProvider(
		loggingnoop.NewLogger(),
		tracingnoop.NewTracerProvider(),
		nil,
		*cfg,
	)

	consumer, err := provider.ProvideConsumer(t.Context(), topic, handlerFunc)
	must.NoError(t, err)

	return consumer
}

func Test_redisConsumer_Consume(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg, containerShutdown, err := BuildContainerBackedRedisConfigForTest(t)
		if err != nil {
			t.Skipf("Skipping test due to container setup failure: %v", err)
		}
		defer func() {
			if containerShutdown != nil {
				test.NoError(t, containerShutdown(ctx))
			}
		}()

		hf := func(context.Context, []byte) error {
			return nil
		}

		consumer := buildRedisBackedConsumer(t, cfg, t.Name(), hf)
		must.NotNil(t, consumer)

		stopChan := make(chan bool, 1)
		errorsChan := make(chan error, 1)
		go consumer.Consume(ctx, stopChan, errorsChan)

		publisher := buildRedisBackedPublisher(t, cfg, t.Name())
		must.NoError(t, publisher.Publish(ctx, []byte("blah")))

		<-time.After(time.Second)
		stopChan <- true
	})

	T.Run("with error handling message", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg, containerShutdown, err := BuildContainerBackedRedisConfigForTest(t)
		if err != nil {
			t.Skipf("Skipping test due to container setup failure: %v", err)
		}
		defer func() {
			if containerShutdown != nil {
				test.NoError(t, containerShutdown(ctx))
			}
		}()

		anticipatedError := errors.New("blah")
		hf := func(context.Context, []byte) error {
			return anticipatedError
		}

		consumer := buildRedisBackedConsumer(t, cfg, t.Name(), hf)
		must.NotNil(t, consumer)

		stopChan := make(chan bool, 1)
		errorsChan := make(chan error, 1)
		go consumer.Consume(ctx, stopChan, errorsChan)

		publisher := buildRedisBackedPublisher(t, cfg, t.Name())
		must.NoError(t, publisher.Publish(ctx, []byte("blah")))

		select {
		case receivedErr := <-errorsChan:
			test.Error(t, receivedErr)
			test.ErrorIs(t, receivedErr, anticipatedError)
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for handler error on errorsChan")
		}

		select {
		case stopChan <- true:
		case <-time.After(time.Second):
		}
	})
}

func Test_consumerProvider_ProvideConsumer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg, containerShutdown, err := BuildContainerBackedRedisConfigForTest(t)
		if err != nil {
			t.Skipf("Skipping test due to container setup failure: %v", err)
		}
		defer func() {
			if containerShutdown != nil {
				test.NoError(t, containerShutdown(ctx))
			}
		}()

		conPro := ProvideRedisConsumerProvider(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, *cfg)
		must.NotNil(t, conPro)

		hf := func(context.Context, []byte) error { return nil }
		actual, err := conPro.ProvideConsumer(ctx, t.Name(), hf)
		test.NoError(t, err)
		test.NotNil(t, actual)
	})

	T.Run("hitting cache", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg, containerShutdown, err := BuildContainerBackedRedisConfigForTest(t)
		if err != nil {
			t.Skipf("Skipping test due to container setup failure: %v", err)
		}
		defer func() {
			if containerShutdown != nil {
				test.NoError(t, containerShutdown(ctx))
			}
		}()

		conPro := ProvideRedisConsumerProvider(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, *cfg)
		must.NotNil(t, conPro)

		hf := func(context.Context, []byte) error { return nil }

		first, err := conPro.ProvideConsumer(ctx, t.Name(), hf)
		test.NoError(t, err)
		must.NotNil(t, first)

		second, err := conPro.ProvideConsumer(ctx, t.Name(), hf)
		test.NoError(t, err)
		must.NotNil(t, second)

		// Second call for the same topic must return the exact same instance
		// from the cache — no second SUBSCRIBE round-trip.
		test.True(t, first == second)
	})

	T.Run("with empty topic", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := Config{
			QueueAddresses: []string{t.Name()},
		}

		conPro := ProvideRedisConsumerProvider(logger, tracingnoop.NewTracerProvider(), nil, cfg)
		must.NotNil(t, conPro)

		actual, err := conPro.ProvideConsumer(t.Context(), "", nil)
		test.Nil(t, actual)
		test.ErrorIs(t, err, ErrEmptyInputProvided)
	})
}

func Test_provideRedisConsumer(T *testing.T) {
	T.Parallel()

	T.Run("panics when NewInt64Counter fails", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricnoop.Int64Counter{}, errors.New("forced error")
			},
		}

		test.Panic(t, func() {
			provideRedisConsumer(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, nil, "t", nil)
		})
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}
