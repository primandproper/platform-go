package sagacfg

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/saga"

	cachememory "github.com/primandproper/primitives-go/cache/memory"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/idempotency"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	metricsnoop "github.com/primandproper/primitives-go/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()

		o := newOptions([]Option{
			WithLogger(logger),
			WithTracerProvider(tracerProvider),
			WithMetricsProvider(metricsProvider),
		})

		test.Eq(t, logger, o.logger)
		test.Eq(t, tracerProvider, o.tracerProvider)
		test.Eq(t, metricsProvider, o.metricsProvider)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})

	T.Run("WithPillars supplies every dependency this package takes", func(t *testing.T) {
		t.Parallel()

		pillars := &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		}

		o := newOptions([]Option{WithPillars(pillars)})

		test.Eq(t, pillars.Logger, o.logger)
		test.Eq(t, pillars.TracerProvider, o.tracerProvider)
		test.Eq(t, pillars.MetricsProvider, o.metricsProvider)
	})

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(nil)})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})

	T.Run("each dependency option sets the field it names", func(t *testing.T) {
		t.Parallel()

		records, err := cachememory.NewInMemoryCache[idempotency.Record[saga.StepResult]](time.Hour)
		must.NoError(t, err)
		t.Cleanup(func() { _ = records.Close() })

		manager, err := idempotency.NewManager(records, newLocker(t))
		must.NoError(t, err)

		var publisher saga.EventPublisher = &stubPublisher{}

		o := newOptions([]Option{
			WithWorkerIdempotency(manager),
			WithWorkerEventPublisher(publisher),
		})

		test.Eq(t, manager, o.manager)
		test.Eq(t, publisher, o.publisher)
	})

	T.Run("a nil dependency is stored as nil", func(t *testing.T) {
		t.Parallel()

		// The option records what it was given; NewWorker is what decides what
		// an absent manager or publisher costs.
		o := newOptions([]Option{
			WithWorkerIdempotency(nil),
			WithWorkerEventPublisher(nil),
		})

		test.Nil(t, o.manager)
		test.Nil(t, o.publisher)
	})

	T.Run("a later option overrides what the pillars supplied", func(t *testing.T) {
		t.Parallel()

		// Options apply in order, which is what lets a caller hand over its
		// pillars and then opt one component out.
		o := newOptions([]Option{
			WithPillars(&observability.Pillars{
				Logger:          loggingnoop.NewLogger(),
				TracerProvider:  tracingnoop.NewTracerProvider(),
				MetricsProvider: metricsnoop.NewMetricsProvider(),
			}),
			WithMetricsProvider(nil),
		})

		test.Nil(t, o.metricsProvider)
		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
	})
}

// stubPublisher is a publisher identity an option test can compare against; a
// func value cannot be, since two func values are only equal to nil.
type stubPublisher struct{}

func (*stubPublisher) Publish(context.Context, database.Tx, ...saga.Event) error { return nil }
