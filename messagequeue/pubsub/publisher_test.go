package pubsub

import (
	"errors"
	"testing"

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

func TestBuildPubSubPublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		publisher := buildPubSubPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), nil, "test-topic")
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
			buildPubSubPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
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
			buildPubSubPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
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
			buildPubSubPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
		})
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func TestProvidePubSubPublisherProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		provider := ProvidePubSubPublisherProvider(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, nil, "test-project")
		must.NotNil(t, provider)
	})
}

func TestPublisherProvider_Ping(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := &publisherProvider{}
		test.NoError(t, p.Ping(t.Context()))
	})
}

func TestPublisherProvider_qualifyTopicName(T *testing.T) {
	T.Parallel()

	T.Run("already qualified", func(t *testing.T) {
		t.Parallel()

		p := &publisherProvider{projectID: "my-project"}
		result := p.qualifyTopicName("projects/my-project/topics/my-topic")
		test.EqOp(t, "projects/my-project/topics/my-topic", result)
	})

	T.Run("unqualified", func(t *testing.T) {
		t.Parallel()

		p := &publisherProvider{projectID: "my-project"}
		result := p.qualifyTopicName("my-topic")
		test.EqOp(t, "projects/my-project/topics/my-topic", result)
	})
}

func TestPublisherProvider_ProvidePublisher(T *testing.T) {
	T.Parallel()

	T.Run("with empty topic", func(t *testing.T) {
		t.Parallel()

		provider := ProvidePubSubPublisherProvider(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, nil, "test-project")

		pub, err := provider.ProvidePublisher(t.Context(), "")
		test.Nil(t, pub)
		test.ErrorIs(t, err, messagequeue.ErrEmptyTopicName)
	})
}
