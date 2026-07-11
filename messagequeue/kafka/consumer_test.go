package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v4/messagequeue"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v4/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

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
	closeCalls         int
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
	m.closeCalls++
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

		// The reader (group membership + connections) must be closed on exit.
		test.EqOp(t, 1, reader.closeCalls)
	})

	T.Run("interrupts a blocked fetch on stop and closes the reader", func(t *testing.T) {
		t.Parallel()

		// FetchMessage blocks until its context is canceled, mimicking a reader waiting
		// for the next message. Signaling stop must unblock it (via ctx cancellation).
		reader := &mockKafkaReader{
			fetchMessageFunc: func(ctx context.Context) (kafka.Message, error) {
				<-ctx.Done()
				return kafka.Message{}, ctx.Err()
			},
		}

		c := &kafkaConsumer{
			reader:          reader,
			o11y:            observability.NewObserverForTest(t.Name()),
			consumedCounter: nil,
			handlerFunc:     func(context.Context, []byte) error { return nil },
		}

		stopChan := make(chan bool, 1)
		done := make(chan struct{})
		go func() {
			c.Consume(t.Context(), stopChan, nil)
			close(done)
		}()

		stopChan <- true

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Consume did not return after stop signal interrupted the blocked fetch")
		}
		test.EqOp(t, 1, reader.closeCalls)
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

		// The message's topic and payload length must have been observed, and the
		// operation should have ended cleanly.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey:  msg.Topic,
			keys.LengthKey: len(msg.Value),
		})
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

		// The topic and payload length must still have been observed, and the
		// handler failure acknowledged on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey:  msg.Topic,
			keys.LengthKey: len(msg.Value),
		})
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

	T.Run("does not commit past a failed message", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		msg1 := kafka.Message{Value: []byte("fails"), Offset: 0}
		msg2 := kafka.Message{Value: []byte("succeeds"), Offset: 1}
		handlerErr := errors.New("handler failed")

		var fetchCount int
		reader := &mockKafkaReader{
			fetchMessageFunc: func(_ context.Context) (kafka.Message, error) {
				fetchCount++
				switch fetchCount {
				case 1:
					return msg1, nil
				case 2:
					return msg2, nil
				default:
					return kafka.Message{}, context.Canceled
				}
			},
			commitMessagesFunc: func(_ context.Context, _ ...kafka.Message) error {
				t.Error("CommitMessages must not be called after a handler failure")
				return nil
			},
		}

		var handlerCalls int
		c := &kafkaConsumer{
			reader:          reader,
			o11y:            observability.NewObserverForTest(t.Name()),
			consumedCounter: metrics.Int64CounterForTest(t, t.Name()),
			handlerFunc: func(context.Context, []byte) error {
				handlerCalls++
				return handlerErr
			},
		}

		errs := make(chan error, 1)

		c.Consume(ctx, make(chan bool, 1), errs)

		// The failed message must not have been committed, and the consumer must not
		// have advanced to (and committed) the following message.
		test.EqOp(t, 0, reader.commitCalls)
		test.EqOp(t, 1, reader.fetchCalls)
		test.EqOp(t, 1, handlerCalls)

		receivedErr := <-errs
		test.ErrorIs(t, receivedErr, handlerErr)
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

		// The topic and payload length must still have been observed, and the
		// commit failure acknowledged on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey:  msg.Topic,
			keys.LengthKey: len(msg.Value),
		})
		test.True(t, op.Ended)
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestNewKafkaConsumerProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		actual := NewKafkaConsumerProvider(
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			nil,
			cfg,
		)
		test.NotNil(t, actual)
	})
}

func Test_consumerProvider_NewConsumer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := NewKafkaConsumerProvider(
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			nil,
			cfg,
		)
		must.NotNil(t, provider)

		hf := func(context.Context, []byte) error { return nil }

		actual, err := provider.NewConsumer(ctx, t.Name(), hf)
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

		provider := NewKafkaConsumerProvider(
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			nil,
			cfg,
		)
		must.NotNil(t, provider)

		actual, err := provider.NewConsumer(ctx, "", nil)
		test.Error(t, err)
		test.ErrorIs(t, err, messagequeue.ErrEmptyTopicName)
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

		provider := NewKafkaConsumerProvider(
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			mp,
			cfg,
		)
		must.NotNil(t, provider)

		hf := func(context.Context, []byte) error { return nil }

		actual, err := provider.NewConsumer(ctx, t.Name(), hf)
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

		provider := NewKafkaConsumerProvider(
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			nil,
			cfg,
		)
		must.NotNil(t, provider)

		hf := func(context.Context, []byte) error { return nil }

		first, err := provider.NewConsumer(ctx, t.Name(), hf)
		test.NoError(t, err)
		test.NotNil(t, first)

		second, err := provider.NewConsumer(ctx, t.Name(), hf)
		test.NoError(t, err)
		test.NotNil(t, second)

		test.True(t, first == second)
	})
}
