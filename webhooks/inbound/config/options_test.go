package inboundcfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/observability"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	"github.com/primandproper/platform-go/v13/webhooks/inbound"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("no options leaves every field unset", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		must.NotNil(t, o)
		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
		test.SliceLen(t, 0, o.verifier)
		test.SliceLen(t, 0, o.receiver)
	})

	T.Run("skips nil options", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithLogger(loggingnoop.NewLogger()), nil})

		must.NotNil(t, o)
		test.NotNil(t, o.logger)
	})

	T.Run("applies every option", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
			WithVerifierOptions(inbound.WithAdditionalSecrets("outgoing")),
			WithReceiverOptions(inbound.WithMaxBodyBytes(1024)),
		})

		must.NotNil(t, o)
		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
		test.NotNil(t, o.metricsProvider)
		test.SliceLen(t, 1, o.verifier)
		test.SliceLen(t, 1, o.receiver)
	})
}

func TestWithPillars(T *testing.T) {
	T.Parallel()

	T.Run("attaches all three at once", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(&observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		})})

		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
		test.NotNil(t, o.metricsProvider)
	})

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(nil)})

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
	})

	// Options apply in order, so a caller can hand over its pillars and then override one.
	T.Run("a later option wins over the pillars", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithPillars(&observability.Pillars{
				Logger:          loggingnoop.NewLogger(),
				MetricsProvider: metricsnoop.NewMetricsProvider(),
			}),
			WithMetricsProvider(nil),
		})

		test.NotNil(t, o.logger)
		test.Nil(t, o.metricsProvider)
	})
}
