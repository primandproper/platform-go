package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/primandproper/platform-go/v13/encoding"
	encodingmock "github.com/primandproper/platform-go/v13/encoding/mock"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/mqmetrics"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/metrics/metricstest"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"

	"github.com/segmentio/kafka-go"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

type mockKafkaWriter struct {
	writeMessagesFunc func(ctx context.Context, msgs ...kafka.Message) error
	closeFunc         func() error
	written           []kafka.Message
	writeCalls        int
	closeCalls        int
}

func (m *mockKafkaWriter) WriteMessages(ctx context.Context, msgs ...kafka.Message) error {
	m.writeCalls++
	m.written = append(m.written, msgs...)
	if m.writeMessagesFunc == nil {
		return nil
	}
	return m.writeMessagesFunc(ctx, msgs...)
}

func (m *mockKafkaWriter) Close() error {
	m.closeCalls++
	if m.closeFunc == nil {
		return nil
	}
	return m.closeFunc()
}

func buildTestPublisher(t *testing.T) (*kafkaPublisher, *mockKafkaWriter, *observability.RecordingObserver) {
	t.Helper()

	writer := &mockKafkaWriter{}

	instruments, err := mqmetrics.NewPublisher(metricsnoop.NewMetricsProvider(), t.Name())
	must.NoError(t, err)

	// Seeded as provideKafkaPublisher seeds it: the topic is stated once at
	// construction, not at each Publish.
	obs := observability.NewRecordingObserverWithValues(map[string]any{keys.TopicKey: t.Name()})

	pub := &kafkaPublisher{
		writer:      writer,
		encoder:     encoding.NewClientEncoder(encoding.ContentTypeJSON),
		o11y:        obs,
		topic:       t.Name(),
		instruments: instruments,
	}

	return pub, writer, obs
}

func Test_kafkaPublisher_Stop(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		pub, writer, _ := buildTestPublisher(t)
		writer.closeFunc = func() error { return nil }

		pub.Stop()

		test.EqOp(t, 1, writer.closeCalls)
	})

	T.Run("with close error", func(t *testing.T) {
		t.Parallel()

		pub, writer, _ := buildTestPublisher(t)
		writer.closeFunc = func() error { return errors.New("close failed") }

		pub.Stop()

		test.EqOp(t, 1, writer.closeCalls)
	})
}

func Test_kafkaPublisher_Publish(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, writer, obs := buildTestPublisher(t)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		writer.writeMessagesFunc = func(_ context.Context, _ ...kafka.Message) error { return nil }

		err := pub.Publish(ctx, inputData)
		test.NoError(t, err)

		test.EqOp(t, 1, writer.writeCalls)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey: pub.topic,
		})
	})

	T.Run("with an ordering key", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, writer, _ := buildTestPublisher(t)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		err := pub.Publish(ctx, inputData, messagequeue.WithOrderingKey("account_123"))
		test.NoError(t, err)

		must.SliceLen(t, 1, writer.written)
		test.Eq(t, []byte("account_123"), writer.written[0].Key)
	})

	T.Run("without an ordering key", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, writer, _ := buildTestPublisher(t)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		err := pub.Publish(ctx, inputData)
		test.NoError(t, err)

		// Nil rather than empty: the balancer treats an empty non-nil key as a
		// value to hash, which would put every unkeyed message on one partition.
		must.SliceLen(t, 1, writer.written)
		test.Nil(t, writer.written[0].Key)
	})

	T.Run("with an empty ordering key", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, writer, _ := buildTestPublisher(t)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		err := pub.Publish(ctx, inputData, messagequeue.WithOrderingKey(""))
		test.NoError(t, err)

		must.SliceLen(t, 1, writer.written)
		test.Nil(t, writer.written[0].Key)
	})

	T.Run("ignores a deduplication key", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, writer, _ := buildTestPublisher(t)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		err := pub.Publish(ctx, inputData, messagequeue.WithDeduplicationKey("event_456"))
		test.NoError(t, err)

		must.SliceLen(t, 1, writer.written)
		test.Nil(t, writer.written[0].Key)
	})

	T.Run("with encoding error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, _, _ := buildTestPublisher(t)

		inputData := &struct {
			Name json.Number `json:"name"`
		}{
			Name: json.Number(t.Name()),
		}

		err := pub.Publish(ctx, inputData)
		test.Error(t, err)
	})

	T.Run("with write error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, writer, obs := buildTestPublisher(t)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		writer.writeMessagesFunc = func(_ context.Context, _ ...kafka.Message) error { return errors.New("write failed") }

		err := pub.Publish(ctx, inputData)
		test.Error(t, err)

		test.EqOp(t, 1, writer.writeCalls)

		// The topic must still have been observed, and the failure recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey: pub.topic,
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with mock encoder error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, _, _ := buildTestPublisher(t)

		enc := &encodingmock.ClientEncoderMock{
			EncodeFunc: func(_ context.Context, _ io.Writer, _ any) error {
				return errors.New("encode failed")
			},
		}
		pub.encoder = enc

		err := pub.Publish(ctx, "something")
		test.Error(t, err)

		test.SliceLen(t, 1, enc.EncodeCalls())
	})
}

func Test_kafkaPublisher_PublishAsync(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, writer, _ := buildTestPublisher(t)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		writer.writeMessagesFunc = func(_ context.Context, _ ...kafka.Message) error { return nil }

		pub.PublishAsync(ctx, inputData)

		test.EqOp(t, 1, writer.writeCalls)
	})

	T.Run("with encoding error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, _, _ := buildTestPublisher(t)

		inputData := &struct {
			Name json.Number `json:"name"`
		}{
			Name: json.Number(t.Name()),
		}

		pub.PublishAsync(ctx, inputData)
	})

	T.Run("with write error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, writer, _ := buildTestPublisher(t)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		writer.writeMessagesFunc = func(_ context.Context, _ ...kafka.Message) error { return errors.New("write failed") }

		pub.PublishAsync(ctx, inputData)

		test.EqOp(t, 1, writer.writeCalls)
	})

	T.Run("forwards an ordering key", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pub, writer, _ := buildTestPublisher(t)

		inputData := &struct {
			Name string `json:"name"`
		}{
			Name: t.Name(),
		}

		pub.PublishAsync(ctx, inputData, messagequeue.WithOrderingKey("account_123"))

		must.SliceLen(t, 1, writer.written)
		test.Eq(t, []byte("account_123"), writer.written[0].Key)
	})
}

// Test_provideKafkaPublisher_balancer guards the half of the ordering fix that
// is not visible in a published message. kafka-go's default balancer is
// RoundRobin, which ignores the key entirely, so a writer left with the default
// would carry every key this package sets and still scatter the messages.
func Test_provideKafkaPublisher_balancer(T *testing.T) {
	T.Parallel()

	T.Run("routes one key to one partition", func(t *testing.T) {
		t.Parallel()

		pub, err := provideKafkaPublisher(nil, nil, nil, []string{"localhost:9092"}, t.Name())
		must.NoError(t, err)

		writer, ok := pub.writer.(*kafka.Writer)
		must.True(t, ok)
		must.NotNil(t, writer.Balancer)

		partitions := []int{0, 1, 2, 3, 4, 5, 6, 7}
		keyed := kafka.Message{Key: []byte("account_123"), Value: []byte(`{}`)}

		first := writer.Balancer.Balance(keyed, partitions...)
		for range 16 {
			test.EqOp(t, first, writer.Balancer.Balance(keyed, partitions...))
		}
	})

	T.Run("separates two keys", func(t *testing.T) {
		t.Parallel()

		pub, err := provideKafkaPublisher(nil, nil, nil, []string{"localhost:9092"}, t.Name())
		must.NoError(t, err)

		writer, ok := pub.writer.(*kafka.Writer)
		must.True(t, ok)

		partitions := []int{0, 1, 2, 3, 4, 5, 6, 7}

		// Two keys chosen because murmur2 puts them on different partitions of
		// eight. The point is that the balancer reads the key at all, which a
		// round-robin balancer does not.
		a := writer.Balancer.Balance(kafka.Message{Key: []byte("account_1")}, partitions...)
		b := writer.Balancer.Balance(kafka.Message{Key: []byte("account_2")}, partitions...)

		test.NotEqOp(t, a, b)
	})
}

func TestNewKafkaPublisherProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		actual := NewKafkaPublisherProvider(cfg)
		test.NotNil(t, actual)
	})
}

func Test_publisherProvider_NewPublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := NewKafkaPublisherProvider(cfg)
		must.NotNil(t, provider)

		actual, err := provider.NewPublisher(ctx, t.Name())
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

		provider := NewKafkaPublisherProvider(cfg)
		must.NotNil(t, provider)

		actual, err := provider.NewPublisher(ctx, "")
		test.Error(t, err)
		test.ErrorIs(t, err, messagequeue.ErrEmptyTopicName)
		test.Nil(t, actual)
	})

	T.Run("with cache hit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := NewKafkaPublisherProvider(cfg)
		must.NotNil(t, provider)

		first, err := provider.NewPublisher(ctx, t.Name())
		test.NoError(t, err)
		test.NotNil(t, first)

		second, err := provider.NewPublisher(ctx, t.Name())
		test.NoError(t, err)
		test.NotNil(t, second)

		test.True(t, first == second)
	})

	T.Run("with error creating published counter", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), errors.New("counter error")
			},
		}

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := NewKafkaPublisherProvider(cfg, WithMetricsProvider(mp))
		must.NotNil(t, provider)

		actual, err := provider.NewPublisher(ctx, t.Name())
		test.Error(t, err)
		test.Nil(t, actual)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating publish error counter", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		mp := &metricsmock.ProviderMock{}
		mp.NewInt64CounterFunc = func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if len(mp.NewInt64CounterCalls()) >= 2 {
				return metricstest.Int64Counter(t, "x"), errors.New("counter error")
			}
			return metricstest.Int64Counter(t, "x"), nil
		}

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := NewKafkaPublisherProvider(cfg, WithMetricsProvider(mp))
		must.NotNil(t, provider)

		actual, err := provider.NewPublisher(ctx, t.Name())
		test.Error(t, err)
		test.Nil(t, actual)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating latency histogram", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), nil
			},
			NewFloat64HistogramFunc: func(_ string, _ ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				return &metrics.Float64HistogramImpl{}, errors.New("histogram error")
			},
		}

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := NewKafkaPublisherProvider(cfg, WithMetricsProvider(mp))
		must.NotNil(t, provider)

		actual, err := provider.NewPublisher(ctx, t.Name())
		test.Error(t, err)
		test.Nil(t, actual)

		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func Test_publisherProvider_Ping(T *testing.T) {
	T.Parallel()

	T.Run("with unreachable broker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			Brokers: []string{"localhost:1"},
			GroupID: "test-group",
		}

		provider := NewKafkaPublisherProvider(cfg)
		must.NotNil(t, provider)

		err := provider.Ping(ctx)
		test.Error(t, err)
	})
}

func Test_publisherProvider_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := NewKafkaPublisherProvider(cfg)
		must.NotNil(t, provider)

		_, err := provider.NewPublisher(ctx, t.Name())
		must.NoError(t, err)

		// Replace cached publisher with one using a mock writer so Close doesn't hit real Kafka
		mw := &mockKafkaWriter{
			closeFunc: func() error { return nil },
		}

		instruments, err := mqmetrics.NewPublisher(metricsnoop.NewMetricsProvider(), t.Name())
		must.NoError(t, err)

		provider.publisherCache[t.Name()] = &kafkaPublisher{
			writer:      mw,
			o11y:        observability.NewObserverForTest(t.Name()),
			instruments: instruments,
		}

		provider.Close()

		test.EqOp(t, 1, mw.closeCalls)
	})

	T.Run("with empty cache", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Brokers: []string{"localhost:9092"},
			GroupID: "test-group",
		}

		provider := NewKafkaPublisherProvider(cfg)
		must.NotNil(t, provider)

		provider.Close()
	})
}
