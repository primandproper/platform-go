package indexing

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/messagequeue"
	msgconfig "github.com/primandproper/platform-go/messagequeue/config"
	mockpublishers "github.com/primandproper/platform-go/messagequeue/mock"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"
	textsearch "github.com/primandproper/platform-go/search/text"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

var testQueuesConfig = &msgconfig.QueuesConfig{SearchIndexRequestsTopicName: "search_index_requests"}

func TestNewIndexScheduler(T *testing.T) {
	T.Parallel()

	T.Run("successful creation", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		// Mock metrics provider
		int64Counter := &mockmetrics.Int64CounterMock{
			AddFunc: func(_ context.Context, _ int64, _ ...metric.AddOption) {},
		}
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return int64Counter, nil
			},
		}

		// Mock message queue provider
		publisher := &mockpublishers.PublisherMock{}
		messageQueueProvider := &mockpublishers.PublisherProviderMock{
			ProvidePublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
				return publisher, nil
			},
		}

		indexFunctions := map[string]Function{
			"test_type": func(ctx context.Context) ([]string, error) {
				return []string{"id1", "id2"}, nil
			},
		}

		scheduler, err := NewIndexScheduler(ctx, logger, tracerProvider, metricsProvider, messageQueueProvider, testQueuesConfig, indexFunctions)
		test.NoError(t, err)
		test.NotNil(t, scheduler)
		test.Eq(t, []string{"test_type"}, scheduler.allIndexTypes)
		test.MapLen(t, 1, scheduler.indexFunctions)

		test.SliceLen(t, 1, metricsProvider.NewInt64CounterCalls())
		test.SliceLen(t, 1, messageQueueProvider.ProvidePublisherCalls())
		test.EqOp(t, testQueuesConfig.SearchIndexRequestsTopicName, messageQueueProvider.ProvidePublisherCalls()[0].Topic)
	})

	T.Run("with nil index functions", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		// Mock metrics provider
		int64Counter := &mockmetrics.Int64CounterMock{
			AddFunc: func(_ context.Context, _ int64, _ ...metric.AddOption) {},
		}
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return int64Counter, nil
			},
		}

		// Mock message queue provider
		publisher := &mockpublishers.PublisherMock{}
		messageQueueProvider := &mockpublishers.PublisherProviderMock{
			ProvidePublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
				return publisher, nil
			},
		}

		scheduler, err := NewIndexScheduler(ctx, logger, tracerProvider, metricsProvider, messageQueueProvider, testQueuesConfig, nil)
		test.NoError(t, err)
		test.NotNil(t, scheduler)
		test.SliceEmpty(t, scheduler.allIndexTypes)
		test.NotNil(t, scheduler.indexFunctions)
		test.MapLen(t, 0, scheduler.indexFunctions)

		test.SliceLen(t, 1, metricsProvider.NewInt64CounterCalls())
		test.SliceLen(t, 1, messageQueueProvider.ProvidePublisherCalls())
	})

	T.Run("metrics provider error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		messageQueueProvider := &mockpublishers.PublisherProviderMock{}

		// Mock metrics provider to return error - need to return a valid interface and error
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return &mockmetrics.Int64CounterMock{}, errors.New("metrics error")
			},
		}

		scheduler, err := NewIndexScheduler(ctx, logger, tracerProvider, metricsProvider, messageQueueProvider, testQueuesConfig, nil)
		test.Error(t, err)
		test.Nil(t, scheduler)
		test.StrContains(t, err.Error(), "metrics error")

		test.SliceLen(t, 1, metricsProvider.NewInt64CounterCalls())
		test.SliceLen(t, 0, messageQueueProvider.ProvidePublisherCalls())
	})

	T.Run("message queue provider error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		// Mock metrics provider
		int64Counter := &mockmetrics.Int64CounterMock{
			AddFunc: func(_ context.Context, _ int64, _ ...metric.AddOption) {},
		}
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return int64Counter, nil
			},
		}

		// Mock message queue provider to return error - need to return a valid interface and error
		messageQueueProvider := &mockpublishers.PublisherProviderMock{
			ProvidePublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
				return &mockpublishers.PublisherMock{}, errors.New("message queue error")
			},
		}

		scheduler, err := NewIndexScheduler(ctx, logger, tracerProvider, metricsProvider, messageQueueProvider, testQueuesConfig, nil)
		test.Error(t, err)
		test.Nil(t, scheduler)
		test.StrContains(t, err.Error(), "message queue error")

		test.SliceLen(t, 1, metricsProvider.NewInt64CounterCalls())
		test.SliceLen(t, 1, messageQueueProvider.ProvidePublisherCalls())
	})
}

func TestIndexScheduler_IndexTypes(T *testing.T) {
	T.Parallel()

	T.Run("successful execution with results", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		// Mock metrics provider
		int64Counter := &mockmetrics.Int64CounterMock{
			AddFunc: func(_ context.Context, _ int64, _ ...metric.AddOption) {},
		}
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return int64Counter, nil
			},
		}

		// Mock message queue provider - all publishes succeed
		publisher := &mockpublishers.PublisherMock{
			PublishFunc: func(_ context.Context, data any) error {
				req, ok := data.(*textsearch.IndexRequest)
				must.True(t, ok)
				test.EqOp(t, "test_type", req.IndexType)
				return nil
			},
		}
		messageQueueProvider := &mockpublishers.PublisherProviderMock{
			ProvidePublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
				return publisher, nil
			},
		}

		// Mock index function
		indexFunctions := map[string]Function{
			"test_type": func(ctx context.Context) ([]string, error) {
				return []string{"id1", "id2", "id3"}, nil
			},
		}

		scheduler, err := NewIndexScheduler(ctx, logger, tracerProvider, metricsProvider, messageQueueProvider, testQueuesConfig, indexFunctions)
		must.NoError(t, err)

		obs := observability.NewRecordingObserver()
		scheduler.o11y = obs

		// Since we only have one index type, it will always be chosen
		err = scheduler.IndexTypes(ctx)
		test.NoError(t, err)

		publishedIDs := collectPublishedRowIDs(t, publisher.PublishCalls())
		test.SliceContainsAll(t, publishedIDs, []string{"id1", "id2", "id3"})

		addCalls := int64Counter.AddCalls()
		test.SliceLen(t, 1, addCalls)
		test.EqOp(t, int64(3), addCalls[0].Incr)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.IndexNameKey: "test_type",
		})
	})

	T.Run("successful execution with empty results", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		// Mock metrics provider
		int64Counter := &mockmetrics.Int64CounterMock{
			AddFunc: func(_ context.Context, _ int64, _ ...metric.AddOption) {},
		}
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return int64Counter, nil
			},
		}

		// Mock message queue provider - no Publish calls expected
		publisher := &mockpublishers.PublisherMock{}
		messageQueueProvider := &mockpublishers.PublisherProviderMock{
			ProvidePublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
				return publisher, nil
			},
		}

		// Mock index function that returns empty results
		indexFunctions := map[string]Function{
			"test_type": func(ctx context.Context) ([]string, error) {
				return []string{}, nil
			},
		}

		scheduler, err := NewIndexScheduler(ctx, logger, tracerProvider, metricsProvider, messageQueueProvider, testQueuesConfig, indexFunctions)
		must.NoError(t, err)

		// No publisher calls expected for empty results
		// But metrics counter is still called with 0
		err = scheduler.IndexTypes(ctx)
		test.NoError(t, err)

		test.SliceLen(t, 0, publisher.PublishCalls())

		addCalls := int64Counter.AddCalls()
		test.SliceLen(t, 1, addCalls)
		test.EqOp(t, int64(0), addCalls[0].Incr)
	})

	T.Run("function returns sql.ErrNoRows", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		// Mock metrics provider
		int64Counter := &mockmetrics.Int64CounterMock{
			AddFunc: func(_ context.Context, _ int64, _ ...metric.AddOption) {},
		}
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return int64Counter, nil
			},
		}

		// Mock message queue provider - no Publish calls expected
		publisher := &mockpublishers.PublisherMock{}
		messageQueueProvider := &mockpublishers.PublisherProviderMock{
			ProvidePublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
				return publisher, nil
			},
		}

		// Mock index function that returns sql.ErrNoRows
		indexFunctions := map[string]Function{
			"test_type": func(ctx context.Context) ([]string, error) {
				return nil, sql.ErrNoRows
			},
		}

		scheduler, err := NewIndexScheduler(ctx, logger, tracerProvider, metricsProvider, messageQueueProvider, testQueuesConfig, indexFunctions)
		must.NoError(t, err)

		// sql.ErrNoRows should be handled gracefully and return nil
		err = scheduler.IndexTypes(ctx)
		test.NoError(t, err)

		test.SliceLen(t, 0, publisher.PublishCalls())
	})

	T.Run("function returns other error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		// Mock metrics provider
		int64Counter := &mockmetrics.Int64CounterMock{
			AddFunc: func(_ context.Context, _ int64, _ ...metric.AddOption) {},
		}
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return int64Counter, nil
			},
		}

		// Mock message queue provider - no Publish calls expected
		publisher := &mockpublishers.PublisherMock{}
		messageQueueProvider := &mockpublishers.PublisherProviderMock{
			ProvidePublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
				return publisher, nil
			},
		}

		// Mock index function that returns an error
		indexFunctions := map[string]Function{
			"test_type": func(ctx context.Context) ([]string, error) {
				return nil, errors.New("database connection failed")
			},
		}

		scheduler, err := NewIndexScheduler(ctx, logger, tracerProvider, metricsProvider, messageQueueProvider, testQueuesConfig, indexFunctions)
		must.NoError(t, err)

		obs := observability.NewRecordingObserver()
		scheduler.o11y = obs

		err = scheduler.IndexTypes(ctx)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "database connection failed")

		test.SliceLen(t, 0, publisher.PublishCalls())

		// The chosen index must still have been observed, and the failure recorded.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.IndexNameKey: "test_type",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("unknown index type", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		// Mock metrics provider
		int64Counter := &mockmetrics.Int64CounterMock{
			AddFunc: func(_ context.Context, _ int64, _ ...metric.AddOption) {},
		}
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return int64Counter, nil
			},
		}

		// Mock message queue provider - no Publish calls expected
		publisher := &mockpublishers.PublisherMock{}
		messageQueueProvider := &mockpublishers.PublisherProviderMock{
			ProvidePublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
				return publisher, nil
			},
		}

		// Create scheduler with empty index functions
		scheduler, err := NewIndexScheduler(ctx, logger, tracerProvider, metricsProvider, messageQueueProvider, testQueuesConfig, map[string]Function{})
		must.NoError(t, err)

		// This should not happen in normal operation since random.Element would return empty string
		// But we can test the error handling by directly calling with a non-existent type
		scheduler.allIndexTypes = []string{"unknown_type"}

		err = scheduler.IndexTypes(ctx)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "unknown index type unknown_type")

		test.SliceLen(t, 0, publisher.PublishCalls())
	})

	T.Run("partial publish failures", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		// Mock metrics provider
		int64Counter := &mockmetrics.Int64CounterMock{
			AddFunc: func(_ context.Context, _ int64, _ ...metric.AddOption) {},
		}
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return int64Counter, nil
			},
		}

		// Mock message queue provider - id2 fails, id1 and id3 succeed
		publishResults := map[string]error{
			"id1": nil,
			"id2": errors.New("publish failed"),
			"id3": nil,
		}
		publisher := &mockpublishers.PublisherMock{
			PublishFunc: func(_ context.Context, data any) error {
				req, ok := data.(*textsearch.IndexRequest)
				must.True(t, ok)
				return publishResults[req.RowID]
			},
		}
		messageQueueProvider := &mockpublishers.PublisherProviderMock{
			ProvidePublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
				return publisher, nil
			},
		}

		// Mock index function
		indexFunctions := map[string]Function{
			"test_type": func(ctx context.Context) ([]string, error) {
				return []string{"id1", "id2", "id3"}, nil
			},
		}

		scheduler, err := NewIndexScheduler(ctx, logger, tracerProvider, metricsProvider, messageQueueProvider, testQueuesConfig, indexFunctions)
		must.NoError(t, err)

		err = scheduler.IndexTypes(ctx)
		test.ErrorContains(t, err, "publish failed") // failed publishes are aggregated and returned

		publishedIDs := collectPublishedRowIDs(t, publisher.PublishCalls())
		test.SliceContainsAll(t, publishedIDs, []string{"id1", "id2", "id3"})

		// Metrics counter should only count successful publishes
		addCalls := int64Counter.AddCalls()
		test.SliceLen(t, 1, addCalls)
		test.EqOp(t, int64(2), addCalls[0].Incr)
	})

	T.Run("all publish failures", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		// Mock metrics provider
		int64Counter := &mockmetrics.Int64CounterMock{
			AddFunc: func(_ context.Context, _ int64, _ ...metric.AddOption) {},
		}
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return int64Counter, nil
			},
		}

		// Mock message queue provider - all publishes fail
		publisher := &mockpublishers.PublisherMock{
			PublishFunc: func(_ context.Context, _ any) error {
				return errors.New("publish failed")
			},
		}
		messageQueueProvider := &mockpublishers.PublisherProviderMock{
			ProvidePublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
				return publisher, nil
			},
		}

		// Mock index function
		indexFunctions := map[string]Function{
			"test_type": func(ctx context.Context) ([]string, error) {
				return []string{"id1", "id2"}, nil
			},
		}

		scheduler, err := NewIndexScheduler(ctx, logger, tracerProvider, metricsProvider, messageQueueProvider, testQueuesConfig, indexFunctions)
		must.NoError(t, err)

		err = scheduler.IndexTypes(ctx)
		test.ErrorContains(t, err, "publish failed") // failed publishes are aggregated and returned

		test.SliceLen(t, 2, publisher.PublishCalls())

		// Metrics counter should count 0 successful publishes
		addCalls := int64Counter.AddCalls()
		test.SliceLen(t, 1, addCalls)
		test.EqOp(t, int64(0), addCalls[0].Incr)
	})
}

func collectPublishedRowIDs(t *testing.T, calls []struct {
	Ctx  context.Context
	Data any
},
) []string {
	t.Helper()
	ids := make([]string, 0, len(calls))
	for i := range calls {
		req, ok := calls[i].Data.(*textsearch.IndexRequest)
		must.True(t, ok)
		ids = append(ids, req.RowID)
	}
	return ids
}
