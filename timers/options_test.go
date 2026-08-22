package timers

import (
	"testing"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewTimerOptions(T *testing.T) {
	T.Parallel()

	T.Run("an empty set leaves everything absent", func(t *testing.T) {
		t.Parallel()

		o := newTimerOptions(nil)

		test.Nil(t, o.clock)
		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
		test.Nil(t, o.keyCodec)
		test.Nil(t, o.wakeup)
	})

	T.Run("nil options are skipped rather than applied", func(t *testing.T) {
		t.Parallel()

		o := newTimerOptions([]Option{nil, WithLogger(logging.EnsureLogger(nil)), nil})

		test.NotNil(t, o.logger)
	})

	// Options apply in order, so a caller can take a bundle and then override
	// one of its parts.
	T.Run("later options win", func(t *testing.T) {
		t.Parallel()

		first := logging.EnsureLogger(nil)
		second := logging.NewNamedLogger(nil, "second")

		o := newTimerOptions([]Option{WithLogger(first), WithLogger(second)})

		test.EqOp(t, second, o.logger)
	})

	T.Run("a nil clock is ignored so the wall clock survives", func(t *testing.T) {
		t.Parallel()

		o := newTimerOptions([]Option{WithClock(nil)})

		test.Nil(t, o.clock)
	})

	T.Run("a clock is held", func(t *testing.T) {
		t.Parallel()

		o := newTimerOptions([]Option{WithClock(clock.NewClock())})

		test.NotNil(t, o.clock)
	})

	T.Run("a nil codec is ignored so the default survives", func(t *testing.T) {
		t.Parallel()

		o := newTimerOptions([]Option{WithKeyCodec[string](nil)})

		test.Nil(t, o.keyCodec)
	})

	T.Run("a codec is held for the constructor to assert", func(t *testing.T) {
		t.Parallel()

		o := newTimerOptions([]Option{WithKeyCodec[string](upperCodec{})})

		must.NotNil(t, o.keyCodec)

		_, ok := o.keyCodec.(KeyCodec[string])
		test.True(t, ok)
	})

	T.Run("a tracer provider is held", func(t *testing.T) {
		t.Parallel()

		provider := tracing.EnsureTracerProvider(nil)

		o := newTimerOptions([]Option{WithTracerProvider(provider)})

		test.EqOp(t, provider, o.tracerProvider)
	})

	T.Run("a metrics provider is held", func(t *testing.T) {
		t.Parallel()

		provider := metrics.EnsureMetricsProvider(nil)

		o := newTimerOptions([]Option{WithMetricsProvider(provider)})

		test.EqOp(t, provider, o.metricsProvider)
	})

	T.Run("a wakeup channel is held", func(t *testing.T) {
		t.Parallel()

		wakeup := make(chan struct{}, 1)

		o := newTimerOptions([]Option{WithWakeup(wakeup)})

		test.NotNil(t, o.wakeup)
	})
}
