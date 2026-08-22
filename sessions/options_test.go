package sessions

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestStoreOptions(T *testing.T) {
	T.Parallel()

	T.Run("defaults are the documented ones", func(t *testing.T) {
		t.Parallel()

		o := newStoreOptions(nil)

		test.EqOp(t, DefaultAbsoluteTimeout, o.absoluteTimeout)
		test.EqOp(t, DefaultIdleTimeout, o.idleTimeout)
		test.EqOp(t, DefaultRetentionGrace, o.grace)
		must.NotNil(t, o.clock)
		must.Nil(t, o.touch)
	})

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()
		var c clock.Clock = clock.NewClock()

		o := newStoreOptions([]Option{
			WithAbsoluteTimeout(time.Hour),
			WithIdleTimeout(time.Minute),
			WithTouchInterval(time.Second),
			WithRetentionGrace(2 * time.Hour),
			WithClock(c),
			WithLogger(logger),
			WithTracerProvider(tracerProvider),
			WithMetricsProvider(metricsProvider),
		})

		test.EqOp(t, time.Hour, o.absoluteTimeout)
		test.EqOp(t, time.Minute, o.idleTimeout)
		must.NotNil(t, o.touch)
		test.EqOp(t, time.Second, *o.touch)
		test.EqOp(t, 2*time.Hour, o.grace)
		test.Eq(t, c, o.clock)
		test.Eq(t, logger, o.logger)
		test.Eq(t, tracerProvider, o.tracerProvider)
		test.Eq(t, metricsProvider, o.metricsProvider)
	})

	// Zero is a meaningful touch interval — refresh on every read — so it has
	// to be distinguishable from having said nothing, which is why the field is
	// a pointer.
	T.Run("a zero touch interval is a choice, not an absence", func(t *testing.T) {
		t.Parallel()

		o := newStoreOptions([]Option{WithTouchInterval(0)})
		must.NotNil(t, o.touch)
		test.EqOp(t, time.Duration(0), *o.touch)
	})

	// A store with no clock cannot expire anything, so the option refuses to
	// remove one rather than accepting a nil that would panic on first use.
	T.Run("a nil clock is ignored", func(t *testing.T) {
		t.Parallel()

		o := newStoreOptions([]Option{WithClock(nil)})
		must.NotNil(t, o.clock)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		o := newStoreOptions([]Option{nil})
		test.EqOp(t, DefaultIdleTimeout, o.idleTimeout)
	})

	// Disabling is a legitimate configuration, so a non-positive value has to
	// reach the policy rather than being treated as unset.
	T.Run("a non-positive timeout disables that deadline", func(t *testing.T) {
		t.Parallel()

		o := newStoreOptions([]Option{WithAbsoluteTimeout(0), WithIdleTimeout(-time.Second)})
		test.EqOp(t, time.Duration(0), o.absoluteTimeout)
		test.EqOp(t, -time.Second, o.idleTimeout)
	})
}
