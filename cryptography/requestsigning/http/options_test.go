package http

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	"github.com/primandproper/platform-go/v13/routing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("defaults", func(t *testing.T) {
		t.Parallel()

		cfg := newConfig()

		test.EqOp(t, DefaultMaxBodySize, cfg.maxBodySize)
		test.Nil(t, cfg.errEncoder)
		test.Nil(t, cfg.logger)
		test.Nil(t, cfg.tracerProvider)
		test.Nil(t, cfg.metricsProvider)
	})

	// There is no unlimited setting: the cap is what makes buffering an
	// unauthenticated caller's body safe, so a non-positive size leaves the
	// default rather than removing the bound.
	T.Run("WithMaxBodySize", func(t *testing.T) {
		t.Parallel()

		cfg := newConfig(WithMaxBodySize(4096))
		test.EqOp(t, int64(4096), cfg.maxBodySize)

		WithMaxBodySize(0)(cfg)
		test.EqOp(t, int64(4096), cfg.maxBodySize)

		WithMaxBodySize(-1)(cfg)
		test.EqOp(t, int64(4096), cfg.maxBodySize)
	})

	T.Run("WithErrorEncoder", func(t *testing.T) {
		t.Parallel()

		cfg := newConfig(WithErrorEncoder(routing.DefaultErrorBody))
		must.NotNil(t, cfg.errEncoder)

		// A nil encoder leaves whatever was there, so the middleware always has
		// something to render a refusal with.
		WithErrorEncoder(nil)(cfg)
		must.NotNil(t, cfg.errEncoder)
	})

	T.Run("the pillars", func(t *testing.T) {
		t.Parallel()

		cfg := newConfig(
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
		)

		must.NotNil(t, cfg.logger)
		must.NotNil(t, cfg.tracerProvider)
		must.NotNil(t, cfg.metricsProvider)
	})
}
