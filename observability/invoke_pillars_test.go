package observability

import (
	"testing"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	"github.com/primandproper/platform-go/v13/observability/profiling"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestPillars_Deps(T *testing.T) {
	T.Parallel()

	T.Run("nil receiver yields three absent dependencies", func(t *testing.T) {
		t.Parallel()

		var p *Pillars

		logger, tracerProvider, metricsProvider := p.Deps()
		test.Nil(t, logger)
		test.Nil(t, tracerProvider)
		test.Nil(t, metricsProvider)
	})

	T.Run("returns what it holds", func(t *testing.T) {
		t.Parallel()

		var l logging.Logger = loggingnoop.NewLogger()

		tp, mp := tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider()

		logger, tracerProvider, metricsProvider := (&Pillars{
			Logger:          l,
			TracerProvider:  tp,
			MetricsProvider: mp,
		}).Deps()

		test.Eq(t, l, logger)
		test.Eq(t, tp, tracerProvider)
		test.Eq(t, mp, metricsProvider)
	})
}

func TestInvokePillars(T *testing.T) {
	T.Parallel()

	T.Run("empty injector is not an error", func(t *testing.T) {
		t.Parallel()

		// The whole point of the change: a container that registers no
		// observability wires up rather than panicking on a MustInvoke.
		pillars, err := InvokePillars(do.New())
		must.NoError(t, err)
		must.NotNil(t, pillars)

		test.Nil(t, pillars.Logger)
		test.Nil(t, pillars.TracerProvider)
		test.Nil(t, pillars.MetricsProvider)
		test.Nil(t, pillars.Profiler)
	})

	T.Run("a registered *Pillars wins outright", func(t *testing.T) {
		t.Parallel()

		want := &Pillars{Logger: loggingnoop.NewLogger()}

		i := do.New()
		do.ProvideValue(i, want)
		// Registered individually too, to prove the bundle takes precedence.
		do.ProvideValue[tracing.Provider](i, tracingnoop.NewTracerProvider())

		pillars, err := InvokePillars(i)
		must.NoError(t, err)
		test.Eq(t, want, pillars)
		test.Nil(t, pillars.TracerProvider)
	})

	T.Run("assembles the pillars registered individually", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[logging.Logger](i, loggingnoop.NewLogger())
		do.ProvideValue[metrics.Provider](i, metricsnoop.NewMetricsProvider())

		pillars, err := InvokePillars(i)
		must.NoError(t, err)
		must.NotNil(t, pillars)

		test.NotNil(t, pillars.Logger)
		test.NotNil(t, pillars.MetricsProvider)
		// Never registered, so still absent rather than defaulted here — the
		// constructor being handed these is what resolves an absent one.
		test.Nil(t, pillars.TracerProvider)
	})

	// Distinguishing a pillar that failed to build from one nobody registered is
	// what keeps a misconfigured exporter from silently degrading to a noop, so
	// every lookup InvokePillars performs is checked for it.
	T.Run("a registered pillar that fails to build is an error", func(t *testing.T) {
		t.Parallel()

		errBuild := errors.New("building the pillar")

		for name, register := range map[string]func(do.Injector){
			"pillars": func(i do.Injector) {
				do.Provide(i, func(do.Injector) (*Pillars, error) { return nil, errBuild })
			},
			"logger": func(i do.Injector) {
				do.Provide(i, func(do.Injector) (logging.Logger, error) { return nil, errBuild })
			},
			"tracer provider": func(i do.Injector) {
				do.Provide(i, func(do.Injector) (tracing.Provider, error) { return nil, errBuild })
			},
			"metrics provider": func(i do.Injector) {
				do.Provide(i, func(do.Injector) (metrics.Provider, error) { return nil, errBuild })
			},
			"profiler": func(i do.Injector) {
				do.Provide(i, func(do.Injector) (profiling.Provider, error) { return nil, errBuild })
			},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				i := do.New()
				register(i)

				pillars, err := InvokePillars(i)
				must.Error(t, err)
				test.ErrorIs(t, err, errBuild)
				test.Nil(t, pillars)
			})
		}
	})
}
