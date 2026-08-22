package workqueuecfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/workqueue"

	"github.com/shoenig/test"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("an empty set leaves everything absent", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
		test.SliceEmpty(t, o.queue)
	})

	T.Run("nil options are skipped", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithLogger(logging.EnsureLogger(nil)), nil})

		test.NotNil(t, o.logger)
	})

	T.Run("a nil pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(nil)})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})

	// Options apply in order, so a caller can hand over its pillars and then
	// leave one component unwired.
	T.Run("an individual option can override a pillar", func(t *testing.T) {
		t.Parallel()

		pillars := &observability.Pillars{Logger: logging.EnsureLogger(nil)}

		o := newOptions([]Option{WithPillars(pillars), WithLogger(nil)})

		test.Nil(t, o.logger)
	})

	T.Run("queue options accumulate", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithQueueOptions(workqueue.WithLogger(nil)),
			WithQueueOptions(workqueue.WithLogger(nil), workqueue.WithMetricsProvider(nil)),
		})

		test.SliceLen(t, 3, o.queue)
	})
}
