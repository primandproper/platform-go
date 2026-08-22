package syncsource

import (
	"testing"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	nooplogging "github.com/primandproper/platform-go/v13/observability/logging/noop"
	noopmetrics "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	nooptracing "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	searchsync "github.com/primandproper/platform-go/v13/search/sync"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("start empty", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)
		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
		test.SliceEmpty(t, o.syncerOptions)
		test.SliceEmpty(t, o.reindexOptions)
	})

	T.Run("apply what they are given", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = nooplogging.NewLogger()
		tracerProvider := nooptracing.NewTracerProvider()
		metricsProvider := noopmetrics.NewMetricsProvider()

		o := newOptions([]Option{
			WithLogger(logger),
			WithTracerProvider(tracerProvider),
			WithMetricsProvider(metricsProvider),
			WithSyncerOptions(searchsync.WithSyncerClock(nil)),
			WithReindexOptions(searchsync.WithReindexBatchSize(7)),
		})

		test.EqOp(t, logger, o.logger)
		test.EqOp(t, tracerProvider, o.tracerProvider)
		test.EqOp(t, metricsProvider, o.metricsProvider)
		test.SliceLen(t, 1, o.syncerOptions)
		test.SliceLen(t, 1, o.reindexOptions)
	})

	T.Run("skip a nil option", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithLogger(nooplogging.NewLogger())})
		must.NotNil(t, o.logger)
	})

	T.Run("accumulate pass-through options rather than replacing them", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithReindexOptions(searchsync.WithReindexBatchSize(1)),
			WithReindexOptions(searchsync.WithReindexBatchSize(2)),
		})

		test.SliceLen(t, 2, o.reindexOptions)
	})
}

func TestWithPillars(T *testing.T) {
	T.Parallel()

	T.Run("attaches all three at once", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = nooplogging.NewLogger()
		tracerProvider := nooptracing.NewTracerProvider()
		metricsProvider := noopmetrics.NewMetricsProvider()

		o := newOptions([]Option{WithPillars(&observability.Pillars{
			Logger:          logger,
			TracerProvider:  tracerProvider,
			MetricsProvider: metricsProvider,
		})})

		test.EqOp(t, logger, o.logger)
		test.EqOp(t, tracerProvider, o.tracerProvider)
		test.EqOp(t, metricsProvider, o.metricsProvider)
	})

	T.Run("attaches nothing from a nil Pillars", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(nil)})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})

	T.Run("is overridable by a later option", func(t *testing.T) {
		t.Parallel()

		// Options apply in order, so a caller can hand over its pillars and
		// then leave one component unwired.
		o := newOptions([]Option{
			WithPillars(&observability.Pillars{
				Logger:          nooplogging.NewLogger(),
				TracerProvider:  nooptracing.NewTracerProvider(),
				MetricsProvider: noopmetrics.NewMetricsProvider(),
			}),
			WithMetricsProvider(nil),
		})

		must.NotNil(t, o.logger)
		test.Nil(t, o.metricsProvider)
	})
}
