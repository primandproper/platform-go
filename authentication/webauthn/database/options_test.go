package database

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/encoding"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("defaults to JSON and the wall clock", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		must.NotNil(t, o.codec)
		test.EqOp(t, string(DefaultSessionContentType), o.codec.ContentType())
		must.NotNil(t, o.clock)
	})

	T.Run("ignores a nil option", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithLogger(loggingnoop.NewLogger())})
		must.NotNil(t, o.logger)
	})

	// Every one of these is "absent means noop" in the other direction: a
	// caller that passes nothing gets the default, and a caller that passes a
	// nil value of a type that has no noop keeps the default rather than
	// panicking later.
	T.Run("keeps its defaults against nil values", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithCodec(nil), WithClock(nil)})

		must.NotNil(t, o.codec)
		must.NotNil(t, o.clock)
	})

	T.Run("takes what it is given", func(t *testing.T) {
		t.Parallel()

		codec := encoding.NewClientEncoder(encoding.ContentTypeCBOR)
		c := clock.NewClock()

		o := newOptions([]Option{
			WithCodec(codec),
			WithClock(c),
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
			WithSweeper(t.Context(), time.Minute),
		})

		test.EqOp(t, string(encoding.ContentTypeCBOR), o.codec.ContentType())
		must.NotNil(t, o.logger)
		must.NotNil(t, o.tracerProvider)
		must.NotNil(t, o.metricsProvider)
		must.NotNil(t, o.sweepCtx)
		test.EqOp(t, time.Minute, o.sweepInterval)
	})
}
