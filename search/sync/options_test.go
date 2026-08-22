package searchsync

import (
	"testing"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	nooplogging "github.com/primandproper/platform-go/v13/observability/logging/noop"
	noopmetrics "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	nooptracing "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("starts with a working clock and the default batch size", func(t *testing.T) {
		t.Parallel()

		o := newOptions()
		must.NotNil(t, o.clock)
		test.EqOp(t, DefaultReindexBatchSize, o.batchSize)
		test.Nil(t, o.pruner)
	})
}

func TestSyncerOptions(T *testing.T) {
	T.Parallel()

	T.Run("apply what they are given", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = nooplogging.NewLogger()
		tracerProvider := nooptracing.NewTracerProvider()
		metricsProvider := noopmetrics.NewMetricsProvider()
		var c clock.Clock = clock.NewClock()

		o := newOptions()
		for _, opt := range []SyncerOption{
			WithSyncerLogger(logger),
			WithSyncerTracerProvider(tracerProvider),
			WithSyncerMetricsProvider(metricsProvider),
			WithSyncerClock(c),
		} {
			opt(o)
		}

		test.EqOp(t, logger, o.logger)
		test.EqOp(t, tracerProvider, o.tracerProvider)
		test.EqOp(t, metricsProvider, o.metricsProvider)
		test.EqOp(t, c, o.clock)
	})

	T.Run("keep the default clock when given a nil one", func(t *testing.T) {
		t.Parallel()

		o := newOptions()
		WithSyncerClock(nil)(o)
		must.NotNil(t, o.clock)
	})
}

func TestReindexOptions(T *testing.T) {
	T.Parallel()

	T.Run("apply what they are given", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = nooplogging.NewLogger()
		tracerProvider := nooptracing.NewTracerProvider()
		metricsProvider := noopmetrics.NewMetricsProvider()
		pruner := &stubEnumerator{}

		o := newOptions()
		for _, opt := range []ReindexOption{
			WithReindexLogger(logger),
			WithReindexTracerProvider(tracerProvider),
			WithReindexMetricsProvider(metricsProvider),
			WithReindexBatchSize(11),
			WithReindexPruner(pruner),
		} {
			opt(o)
		}

		test.EqOp(t, logger, o.logger)
		test.EqOp(t, tracerProvider, o.tracerProvider)
		test.EqOp(t, metricsProvider, o.metricsProvider)
		test.EqOp(t, 11, o.batchSize)
		test.EqOp(t, Enumerator(pruner), o.pruner)
	})

	T.Run("keep the default batch size when given a non-positive one", func(t *testing.T) {
		t.Parallel()

		o := newOptions()
		WithReindexBatchSize(0)(o)
		test.EqOp(t, DefaultReindexBatchSize, o.batchSize)

		WithReindexBatchSize(-1)(o)
		test.EqOp(t, DefaultReindexBatchSize, o.batchSize)
	})

	T.Run("keep no pruner when given a nil one", func(t *testing.T) {
		t.Parallel()

		// Taking it would mean pruning against an index that reports holding
		// nothing, which deletes everything.
		o := newOptions()
		WithReindexPruner(nil)(o)
		test.Nil(t, o.pruner)
	})
}
