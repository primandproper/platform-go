package timerscfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/timers"

	"github.com/shoenig/test"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("an empty set leaves everything absent", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		test.Nil(t, o.clock)
		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
		test.SliceEmpty(t, o.timers)
	})

	T.Run("nil options are skipped rather than applied", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithLogger(logging.EnsureLogger(nil)), nil})

		test.NotNil(t, o.logger)
	})

	// A nil Pillars attaches nothing rather than panicking, so a caller with no
	// observability can still hand over whatever it has.
	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(nil)})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})

	// Options apply in order, so a caller can take a bundle and then override
	// one of its parts.
	T.Run("later options win", func(t *testing.T) {
		t.Parallel()

		second := logging.NewNamedLogger(nil, "second")

		o := newOptions([]Option{WithLogger(logging.EnsureLogger(nil)), WithLogger(second)})

		test.EqOp(t, second, o.logger)
	})

	T.Run("a clock is held", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithClock(clock.NewClock())})

		test.NotNil(t, o.clock)
	})

	T.Run("passthrough options accumulate", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithTimerOptions(timers.WithLogger(nil)),
			WithTimerOptions(timers.WithTracerProvider(nil), timers.WithMetricsProvider(nil)),
		})

		test.SliceLen(t, 3, o.timers)
	})
}
