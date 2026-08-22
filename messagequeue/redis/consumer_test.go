package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

// buildRedisBackedConsumer builds a Redis container-backed messagequeue.Consumer.
func buildRedisBackedConsumer(t *testing.T, cfg *Config, topic string, handlerFunc func(context.Context, []byte) error) messagequeue.Consumer {
	t.Helper()

	provider, err := NewRedisConsumerProvider(t.Context(), *cfg)
	must.NoError(t, err)

	consumer, err := provider.NewConsumer(t.Context(), topic, handlerFunc)
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

		actual, isConsumer := consumer.(*redisConsumer)
		must.True(t, isConsumer)

		// Seeded as provideRedisConsumer seeds it: the subscription is to one
		// topic, so the topic is stated at construction rather than read off
		// each message.
		obs := observability.NewRecordingObserverWithValues(map[string]any{keys.TopicKey: t.Name()})
		actual.o11y = obs

		errorsChan := make(chan error, 1)
		done := make(chan struct{})
		consumeCtx, stopConsuming := context.WithCancel(ctx)
		defer stopConsuming()
		go func() {
			consumer.Consume(consumeCtx, errorsChan)
			close(done)
		}()

		publisher := buildRedisBackedPublisher(t, cfg, t.Name())
		must.NoError(t, publisher.Publish(ctx, []byte("blah")))

		<-time.After(time.Second)
		stopConsuming()
		// Wait for Consume to return so its observations are visible here without
		// racing the consume goroutine.
		<-done

		// The consume opened and ended an observed operation carrying the topic
		// and the (encoded) payload length, with no recorded error on the happy
		// path. The length reflects the on-the-wire payload, not the raw input.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey: t.Name(),
		})
		op.Observed(t, observability.ObservedKeyFunc(keys.LengthKey, func(v any) bool {
			n, ok := v.(int)
			return ok && n > 0
		}))
		test.SliceEmpty(t, op.Errors)
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

		actual, isConsumer := consumer.(*redisConsumer)
		must.True(t, isConsumer)

		// Seeded as provideRedisConsumer seeds it: the subscription is to one
		// topic, so the topic is stated at construction rather than read off
		// each message.
		obs := observability.NewRecordingObserverWithValues(map[string]any{keys.TopicKey: t.Name()})
		actual.o11y = obs

		errorsChan := make(chan error, 1)
		done := make(chan struct{})
		consumeCtx, stopConsuming := context.WithCancel(ctx)
		defer stopConsuming()
		go func() {
			consumer.Consume(consumeCtx, errorsChan)
			close(done)
		}()

		publisher := buildRedisBackedPublisher(t, cfg, t.Name())
		must.NoError(t, publisher.Publish(ctx, []byte("blah")))

		select {
		case receivedErr := <-errorsChan:
			test.Error(t, receivedErr)
			test.ErrorIs(t, receivedErr, anticipatedError)
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for handler error on errorsChan")
		}

		// Stop the consumer and wait for Consume to return — the error is sent on
		// errorsChan before op.End(), so we must let the goroutine finish before
		// asserting, both for End and to avoid racing its observations.
		select {
		default:
			stopConsuming()
		case <-time.After(time.Second):
		}
		<-done

		// The consume opened and ended an observed operation carrying the topic
		// and the (encoded) payload length, and recorded the handler failure on it.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey: t.Name(),
		})
		op.Observed(t, observability.ObservedKeyFunc(keys.LengthKey, func(v any) bool {
			n, ok := v.(int)
			return ok && n > 0
		}))
		must.SliceLen(t, 1, op.Errors)
		test.ErrorIs(t, op.Errors[0], anticipatedError)
	})
}

func Test_consumerProvider_NewConsumer(T *testing.T) {
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

		conPro, err := NewRedisConsumerProvider(t.Context(), *cfg)
		must.NoError(t, err)
		must.NotNil(t, conPro)

		hf := func(context.Context, []byte) error { return nil }
		actual, err := conPro.NewConsumer(ctx, t.Name(), hf)
		test.NoError(t, err)
		test.NotNil(t, actual)
	})

	T.Run("rejects a second consumer for the same topic", func(t *testing.T) {
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

		conPro, err := NewRedisConsumerProvider(t.Context(), *cfg)
		must.NoError(t, err)
		must.NotNil(t, conPro)

		hf := func(context.Context, []byte) error { return nil }

		first, err := conPro.NewConsumer(ctx, t.Name(), hf)
		test.NoError(t, err)
		must.NotNil(t, first)

		// The second caller used to get the first caller's consumer, wired to the
		// first caller's handler — so their own handler never saw a message, with
		// nothing failing and nothing logged.
		second, err := conPro.NewConsumer(ctx, t.Name(), hf)
		test.ErrorIs(t, err, messagequeue.ErrConsumerAlreadyRegistered)
		test.Nil(t, second)
	})

	T.Run("with empty topic", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			QueueAddresses: []string{t.Name()},
		}

		conPro, err := NewRedisConsumerProvider(t.Context(), cfg)
		must.NoError(t, err)
		must.NotNil(t, conPro)

		actual, err := conPro.NewConsumer(t.Context(), "", nil)
		test.Nil(t, actual)
		test.ErrorIs(t, err, messagequeue.ErrEmptyTopicName)
	})
}

func Test_provideRedisConsumer(T *testing.T) {
	T.Parallel()

	T.Run("returns error when NewInt64Counter fails", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricnoop.Int64Counter{}, errors.New("forced error")
			},
		}

		actual, err := provideRedisConsumer(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, nil, "t", nil)
		test.Error(t, err)
		test.Nil(t, actual)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}
