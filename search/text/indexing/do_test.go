package indexing

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v6/messagequeue"
	msgconfig "github.com/primandproper/platform-go/v6/messagequeue/config"
	mockpublishers "github.com/primandproper/platform-go/v6/messagequeue/mock"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v6/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	otelmetric "go.opentelemetry.io/otel/metric"
)

func TestRegisterIndexScheduler(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		int64Counter := &mockmetrics.Int64CounterMock{}
		metricsProvider := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...otelmetric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "indexer.handled_records", counterName)
				return int64Counter, nil
			},
		}

		publisher := &mockpublishers.PublisherMock{}
		messageQueueProvider := &mockpublishers.PublisherProviderMock{
			NewPublisherFunc: func(_ context.Context, _ string) (messagequeue.Publisher, error) {
				return publisher, nil
			},
		}

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, metricsProvider)
		do.ProvideValue[messagequeue.PublisherProvider](i, messageQueueProvider)
		do.ProvideValue(i, &msgconfig.QueuesConfig{SearchIndexRequestsTopicName: "test_topic"})
		do.ProvideValue(i, map[string]Function{
			"test": func(ctx context.Context) ([]string, error) {
				return nil, nil
			},
		})

		RegisterIndexScheduler(i)

		scheduler, err := do.Invoke[*IndexScheduler](i)
		must.NoError(t, err)
		test.NotNil(t, scheduler)

		test.SliceLen(t, 1, metricsProvider.NewInt64CounterCalls())
		test.SliceLen(t, 1, messageQueueProvider.NewPublisherCalls())
		test.EqOp(t, "test_topic", messageQueueProvider.NewPublisherCalls()[0].Topic)
	})
}
