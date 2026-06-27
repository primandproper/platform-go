package kafka

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/observability"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/segmentio/kafka-go"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

type mockKafkaReader struct {
	fetchMessageFunc   func(ctx context.Context) (kafka.Message, error)
	commitMessagesFunc func(ctx context.Context, msgs ...kafka.Message) error
	closeFunc          func() error
	fetchCalls         int
	commitCalls        int
}

func (m *mockKafkaReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	m.fetchCalls++
	return m.fetchMessageFunc(ctx)
}

func (m *mockKafkaReader) CommitMessages(ctx context.Context, msgs ...kafka.Message) error {
	m.commitCalls++
	return m.commitMessagesFunc(ctx, msgs...)
}

func (m *mockKafkaReader) Close() error {
	if m.closeFunc == nil {
		return nil
	}
	return m.closeFunc()
}

func Test_kafkaConsumer_Consume(T *testing.T) {
	T.Parallel()

	T.Run("stops on context cancellation", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		reader := &mockKafkaReader{
			fetchMessageFunc: func(_ context.Context) (kafka.Message, error) {
				return kafka.Message{}, context.Canceled
			},
		}

		c := &kafkaConsumer{
			reader:          reader,
			o11y:            observability.NewObserverForTest(t.Name()),
			consumedCounter: nil,
			handlerFunc: func(context.Context, []byte) error {
				return nil
			},
		}

		stopChan := make(chan bool, 1)
		errs := make(chan error, 1)

		cancel()
		c.Consume(ctx, stopChan, errs)
	})

	T.Run("stops on stop channel signal", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		reader := &mockKafkaReader{
			fetchMessageFunc: func(_ context.Context) (kafka.Message, error) {
				return kafka.Message{}, context.Canceled
			},
		}

		c := &kafkaConsumer{
			reader:          reader,
			o11y:            observability.NewObserverForTest(t.Name()),
			consumedCounter: nil,
			handlerFunc: func(context.Context, []byte) error {
				return nil
			},
		}

		stopChan := make(chan bool, 1)
		errs := make(chan error, 1)

		stopChan <- true
		c.Consume(ctx, stopChan, errs)
	})

	T.Run("with nil stop channel", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		reader := &mockKafkaReader{
			fetchMessageFunc: func(_ context.Context) (kafka.Message, error) {
				return kafka.Message{}, context.Canceled
			},
		}

		c := &kafkaConsumer{
			reader:          reader,
			o11y:            observability.NewObserverForTest(t.Name()),
			consumedCounter: nil,
			handlerFunc: func(context.Context, []byte) error {
				return nil
			},
		}

		errs := make(chan error, 1)

		cancel()
		c.Consume(ctx, nil, errs)
	})

	T.Run("with fetch error and context still alive", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		fetchErr := errors.New("fetch failed")
		callCount := 0

		reader := &mockKafkaReader{
			fetchMessageFunc: func(_ context.Context) (kafka.Message, error) {
				callCount++
				if callCount >= 2 {
					cancel()
				}
				return kafka.Message{}, fetchErr
			},
		}

		c := &kafkaConsumer{
			reader:          reader,
			o11y:            observability.NewObserverForTest(t.Name()),
			consumedCounter: nil,
			handlerFunc: func(context.Context, []byte) error {
				return nil
			},
		}

		stopChan := make(chan bool, 1)
		errs := make(chan error, 10)

		c.Consume(ctx, stopChan, errs)

		select {
		case receivedErr := <-errs:
			test.ErrorIs(t, receivedErr, fetchErr)
		default:
			t.Error("expected an error on the errors channel")
		}
	})

	T.Run("with fetch error and nil errors channel", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		fetchErr := errors.New("fetch failed")

		reader := &mockKafkaReader{
			fetchMessageFunc: func(_ context.Context) (kafka.Message, error) {
				cancel()
				return kafka.Message{}, fetchErr
			},
		}

		c := &kafkaConsumer{
			reader:          reader,
			o11y:            observability.NewObserverForTest(t.Name()),
			consumedCounter: nil,
			handlerFunc: func(context.Context, []byte) error {
				return nil
			},
		}

		stopChan := make(chan bool, 1)

		c.Consume(ctx, stopChan, nil)
	})

	T.Run("with successful message handling", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		msg := kafka.Message{Value: []byte("test-message")}

		var fetchCount int
		reader := &mockKafkaReader{
			fetchMessageFunc: func(_ context.Context) (kafka.Message, error) {
				fetchCount++
				if fetchCount == 1 {
					return msg, nil
				}
				return kafka.Message{}, context.Canceled
			},
			commitMessagesFunc: func(_ context.Context, msgs ...kafka.Message) error {
				must.SliceLen(t, 1, msgs)
				test.Eq(t, msg, msgs[0])
				return nil
			},
		}

		obs := observability.NewRecordingObserver()

		handlerCalled := false
		c := &kafkaConsumer{
			reader:          reader,
			o11y:            obs,
			consumedCounter: metrics.Int64CounterForTest(t, t.Name()),
			handlerFunc: func(_ context.Context, data []byte) error {
				handlerCalled = true
				test.Eq(t, []byte("test-message"), data)
				cancel()
				return nil
			},
		}

		stopChan := make(chan bool, 1)
		errs := make(chan error, 10)

		c.Consume(ctx, stopChan, errs)
		test.True(t, handlerCalled)
		test.EqOp(t, 1, reader.commitCalls)

		// The message was handled and committed, so the operation should have ended cleanly.
		op := obs.Operations[0]
		test.True(t, op.Ended)
		test.SliceEmpty(t, op.Errors)
	})

	T.Run("with handler error", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		msg := kafka.Message{Value: []byte("test-message")}
		handlerErr := errors.New("handler failed")

		var fetchCount int
		reader := &mockKafkaReader{
			fetchMessageFunc: func(_ context.Context) (kafka.Message, error) {
				fetchCount++
				if fetchCount == 1 {
					return msg, nil
				}
				return kafka.Message{}, context.Canceled
			},
		}

		obs := observability.NewRecordingObserver()

		c := &kafkaConsumer{
			reader:          reader,
			o11y:            obs,
			consumedCounter: metrics.Int64CounterForTest(t, t.Name()),
			handlerFunc: func(context.Context, []byte) error {
				cancel()
				return handlerErr
			},
		}

		stopChan := make(chan bool, 1)
		errs := make(chan error, 10)

		c.Consume(ctx, stopChan, errs)

		receivedErr := <-errs
		test.Error(t, receivedErr)
		test.ErrorIs(t, receivedErr, handlerErr)

		// The handler failure must have been acknowledged on the operation.
		op := obs.Operations[0]
		test.True(t, op.Ended)
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with handler error and nil errors channel", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		msg := kafka.Message{Value: []byte("test-message")}

		var fetchCount int
		reader := &mockKafkaReader{
			fetchMessageFunc: func(_ context.Context) (kafka.Message, error) {
				fetchCount++
				if fetchCount == 1 {
					return msg, nil
				}
				return kafka.Message{}, context.Canceled
			},
		}

		c := &kafkaConsumer{
			reader:          reader,
			o11y:            observability.NewObserverForTest(t.Name()),
			consumedCounter: metrics.Int64CounterForTest(t, t.Name()),
			handlerFunc: func(context.Context, []byte) error {
				cancel()
				return errors.New("handler failed")
			},
		}

		stopChan := make(chan bool, 1)

		c.Consume(ctx, stopChan, nil)
	})

	T.Run("with commit error", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		msg := kafka.Message{Value: []byte("test-message")}

		var fetchCount int
		reader := &mockKafkaReader{
			fetchMessageFunc: func(_ context.Context) (kafka.Message, error) {
				fetchCount++
				if fetchCount == 1 {
					return msg, nil
				}
				return kafka.Message{}, context.Canceled
			},
			commitMessagesFunc: func(_ context.Context, _ ...kafka.Message) error {
				return errors.New("commit failed")
			},
		}

		obs := observability.NewRecordingObserver()

		c := &kafkaConsumer{
			reader:          reader,
			o11y:            obs,
			consumedCounter: metrics.Int64CounterForTest(t, t.Name()),
			handlerFunc: func(context.Context, []byte) error {
				cancel()
				return nil
			},
		}

		stopChan := make(chan bool, 1)
		errs := make(chan error, 10)

		c.Consume(ctx, stopChan, errs)
		test.EqOp(t, 1, reader.commitCalls)

		// The commit failure must have been acknowledged on the operation.
		op := obs.Operations[0]
		test.True(t, op.Ended)
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestProvideKafkaConsumerProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		actual := ProvideKafkaConsumerProvider(
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			nil,
			cfg,
		)
		test.NotNil(t, actual)
	})
}

func Test_consumerProvider_ProvideConsumer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := ProvideKafkaConsumerProvider(
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			nil,
			cfg,
		)
		must.NotNil(t, provider)

		hf := func(context.Context, []byte) error { return nil }

		actual, err := provider.ProvideConsumer(ctx, t.Name(), hf)
		test.NoError(t, err)
		test.NotNil(t, actual)
	})

	T.Run("with empty topic", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := ProvideKafkaConsumerProvider(
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			nil,
			cfg,
		)
		must.NotNil(t, provider)

		actual, err := provider.ProvideConsumer(ctx, "", nil)
		test.Error(t, err)
		test.ErrorIs(t, err, ErrEmptyInputProvided)
		test.Nil(t, actual)
	})

	T.Run("with error creating consumed counter", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metrics.Int64CounterForTest(t, "x"), errors.New("counter error")
			},
		}

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := ProvideKafkaConsumerProvider(
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			mp,
			cfg,
		)
		must.NotNil(t, provider)

		hf := func(context.Context, []byte) error { return nil }

		actual, err := provider.ProvideConsumer(ctx, t.Name(), hf)
		test.Error(t, err)
		test.Nil(t, actual)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with cache hit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := ProvideKafkaConsumerProvider(
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			nil,
			cfg,
		)
		must.NotNil(t, provider)

		hf := func(context.Context, []byte) error { return nil }

		first, err := provider.ProvideConsumer(ctx, t.Name(), hf)
		test.NoError(t, err)
		test.NotNil(t, first)

		second, err := provider.ProvideConsumer(ctx, t.Name(), hf)
		test.NoError(t, err)
		test.NotNil(t, second)

		test.True(t, first == second)
	})
}
