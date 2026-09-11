package operations

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	metricsnoop "github.com/primandproper/primitives-go/v2/observability/metrics/noop"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// pillars are the three dependencies every option group here carries, built
// once so a test can assert the one it set is the one that landed.
func pillars() (logger logging.Logger, tracerProvider tracing.Provider, metricsProvider metrics.Provider) {
	return loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider()
}

func TestSQLStoreOptions(T *testing.T) {
	T.Parallel()

	apply := func(opts ...SQLStoreOption) *SQLStore {
		s := &SQLStore{}
		for _, opt := range opts {
			opt(s)
		}

		return s
	}

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		logger, tracerProvider, metricsProvider := pillars()

		s := apply(
			WithStoreLogger(logger),
			WithStoreTracerProvider(tracerProvider),
			WithStoreMetricsProvider(metricsProvider),
			WithStoreTablePrefix("ops_"),
			WithStoreNotifyChannel("operations_changed"),
		)

		test.Eq(t, logger, s.logger)
		test.Eq(t, tracerProvider, s.tracerProvider)
		test.Eq(t, metricsProvider, s.metricsProvider)
		test.EqOp(t, "ops_", s.tablePrefix)
		test.EqOp(t, "operations_changed", s.notifyChannel)
	})

	T.Run("naming none of them leaves the store unadorned", func(t *testing.T) {
		t.Parallel()

		s := apply()

		test.Nil(t, s.logger)
		test.Nil(t, s.tracerProvider)
		test.Nil(t, s.metricsProvider)
		test.EqOp(t, "", s.notifyChannel)
	})

	T.Run("the notify channel is what turns the watch path from a poll into a push", func(t *testing.T) {
		t.Parallel()

		// Without one the watch path still delivers every state an operation
		// passes through, a poll interval late — so an unset channel is a
		// configuration rather than a defect, and has to stay expressible.
		test.EqOp(t, "", apply(WithStoreNotifyChannel("")).notifyChannel)
	})
}

func TestServiceOptions(T *testing.T) {
	T.Parallel()

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		logger, tracerProvider, metricsProvider := pillars()

		o := newServiceOptions([]ServiceOption{
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

		o := newServiceOptions([]ServiceOption{nil})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})
}

func TestWorkerOptions(T *testing.T) {
	T.Parallel()

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		logger, tracerProvider, metricsProvider := pillars()

		o := newWorkerOptions([]WorkerOption{
			WithWorkerLogger(logger),
			WithWorkerTracerProvider(tracerProvider),
			WithWorkerMetricsProvider(metricsProvider),
		})

		test.Eq(t, logger, o.logger)
		test.Eq(t, tracerProvider, o.tracerProvider)
		test.Eq(t, metricsProvider, o.metricsProvider)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		o := newWorkerOptions([]WorkerOption{nil})

		test.Nil(t, o.logger)
		test.Nil(t, o.metricsProvider)
	})
}

func TestWatcherOptions(T *testing.T) {
	T.Parallel()

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		logger, tracerProvider, metricsProvider := pillars()
		wakeup := make(chan struct{})

		o := newWatcherOptions([]WatcherOption{
			WithWatcherLogger(logger),
			WithWatcherTracerProvider(tracerProvider),
			WithWatcherMetricsProvider(metricsProvider),
			WithWatcherWakeup(wakeup),
		})

		test.Eq(t, logger, o.logger)
		test.Eq(t, tracerProvider, o.tracerProvider)
		test.Eq(t, metricsProvider, o.metricsProvider)
		must.NotNil(t, o.wakeup)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		o := newWatcherOptions([]WatcherOption{nil})

		test.Nil(t, o.logger)
		test.Nil(t, o.wakeup)
	})

	T.Run("no wakeup leaves the watch path a plain poll", func(t *testing.T) {
		t.Parallel()

		// Every guarantee the watch path makes is unchanged without one: a
		// subscriber receives a snapshot of the row rather than a stream of
		// changes to it, so it still sees every state the operation reaches.
		o := newWatcherOptions(nil)

		test.Nil(t, o.wakeup)
	})
}

func TestStartOptions(T *testing.T) {
	T.Parallel()

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		o := newStartOptions([]StartOption{
			WithOwner("user-1"),
			WithPriority(9),
			WithDelay(time.Hour),
			WithID("op-1"),
		})

		test.EqOp(t, "user-1", o.owner)
		test.EqOp(t, 9, o.priority)
		test.EqOp(t, time.Hour, o.delay)
		test.EqOp(t, "op-1", o.id)
	})

	T.Run("an unadorned Start carries none of them", func(t *testing.T) {
		t.Parallel()

		// Every Start being a new operation is the right default; WithID is what
		// a handler behind a retrying client reaches for instead.
		o := newStartOptions(nil)

		test.EqOp(t, "", o.owner)
		test.EqOp(t, 0, o.priority)
		test.EqOp(t, time.Duration(0), o.delay)
		test.EqOp(t, "", o.id)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		o := newStartOptions([]StartOption{nil, WithOwner("user-1"), nil})

		test.EqOp(t, "user-1", o.owner)
	})

	T.Run("a later option wins", func(t *testing.T) {
		t.Parallel()

		// They are per-call rather than per-service, so the last word about one
		// request belongs to whoever assembled that call.
		o := newStartOptions([]StartOption{WithPriority(1), WithPriority(5)})

		test.EqOp(t, 5, o.priority)
	})
}
