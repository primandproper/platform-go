package database

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("defaults to CBOR and the wall clock", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		must.NotNil(t, o.codec)
		test.EqOp(t, string(DefaultPayloadContentType), o.codec.ContentType())
		must.NotNil(t, o.clock)
	})

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		var (
			codec encoding.Codec = encoding.NewClientEncoder(encoding.ContentTypeJSON)
			c     clock.Clock    = newFakeClock()
		)
		var logger logging.Logger = loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()

		o := newOptions([]Option{
			WithCodec(codec),
			WithClock(c),
			WithLogger(logger),
			WithTracerProvider(tracerProvider),
			WithMetricsProvider(metricsProvider),
			WithSweeper(context.Background(), time.Minute),
		})

		test.Eq(t, codec, o.codec)
		test.Eq(t, c, o.clock)
		test.Eq(t, logger, o.logger)
		test.Eq(t, tracerProvider, o.tracerProvider)
		test.Eq(t, metricsProvider, o.metricsProvider)
		must.NotNil(t, o.sweepCtx)
		test.EqOp(t, time.Minute, o.sweepInterval)
	})

	// A nil codec or clock would panic on first use, so the options refuse to
	// remove one rather than accepting an argument that cannot work.
	T.Run("nil replacements are ignored", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithCodec(nil), WithClock(nil)})

		must.NotNil(t, o.codec)
		must.NotNil(t, o.clock)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil})
		must.NotNil(t, o.codec)
	})
}
