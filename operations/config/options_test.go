package operationscfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/operations"

	"github.com/shoenig/test"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithLogger(logging.EnsureLogger(nil)), nil})

		test.NotNil(t, o.logger)
	})

	T.Run("the last option wins", func(t *testing.T) {
		t.Parallel()

		first := logging.NewNamedLogger(nil, "first")
		second := logging.NewNamedLogger(nil, "second")

		o := newOptions([]Option{WithLogger(first), WithLogger(second)})

		test.Eq(t, second, o.logger)
	})

	// Options apply in order, so a caller can hand over its pillars and then
	// override one of them — which is how a component is left unmetered while
	// the rest of the process is fully instrumented.
	T.Run("WithPillars can be overridden afterwards", func(t *testing.T) {
		t.Parallel()

		pillars := &observability.Pillars{}

		o := newOptions([]Option{WithPillars(pillars), WithMetricsProvider(nil)})

		test.Nil(t, o.metricsProvider)
	})

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(nil)})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})

	// The two wakeups are separate because they want different channels even
	// when both come from Postgres: one fires when work is enqueued, the other
	// when a row changes. Sharing one would wake every watcher on every enqueue.
	T.Run("the two wakeups are distinct", func(t *testing.T) {
		t.Parallel()

		queueWake := make(chan struct{})
		watcherWake := make(chan struct{})

		o := newOptions([]Option{
			WithQueueWakeup(queueWake),
			WithWatcherWakeup(watcherWake),
		})

		test.NotNil(t, o.queueWakeup)
		test.NotNil(t, o.watcherWakeup)
	})

	T.Run("pass-through options accumulate", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithStoreOptions(operations.WithStoreTablePrefix("a")),
			WithStoreOptions(operations.WithStoreTablePrefix("b")),
			WithServiceOptions(operations.WithLogger(nil)),
			WithWorkerOptions(operations.WithWorkerLogger(nil)),
			WithWatcherOptions(operations.WithWatcherLogger(nil)),
		})

		test.SliceLen(t, 2, o.store)
		test.SliceLen(t, 1, o.service)
		test.SliceLen(t, 1, o.worker)
		test.SliceLen(t, 1, o.watcher)
	})
}
