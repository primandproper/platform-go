package observability

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/observability/logging"
	loggingcfg "github.com/primandproper/platform-go/v14/observability/logging/config"
	metricscfg "github.com/primandproper/platform-go/v14/observability/metrics/config"
	profilingcfg "github.com/primandproper/platform-go/v14/observability/profiling/config"
	tracingcfg "github.com/primandproper/platform-go/v14/observability/tracing/config"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterO11yConfigs(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		i := do.New()
		do.ProvideValue(i, cfg)

		RegisterO11yConfigs(i)

		loggingConfig, err := do.Invoke[*loggingcfg.Config](i)
		must.NoError(t, err)
		test.NotNil(t, loggingConfig)

		metricsConfig, err := do.Invoke[*metricscfg.Config](i)
		must.NoError(t, err)
		test.NotNil(t, metricsConfig)

		tracingConfig, err := do.Invoke[*tracingcfg.Config](i)
		must.NoError(t, err)
		test.NotNil(t, tracingConfig)

		profilingConfig, err := do.Invoke[*profilingcfg.Config](i)
		must.NoError(t, err)
		test.NotNil(t, profilingConfig)
	})
}

// TestInvokePillarsRegisteredButUnbuildable is the half of InvokePillars that
// invoke_pillars_test.go does not reach: a *Pillars that is registered and
// cannot be built, by either of the two routes do reports.
func TestInvokePillarsRegisteredButUnbuildable(T *testing.T) {
	T.Parallel()

	T.Run("a *Pillars provider that fails to build is an error", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("the collector is unreachable")

		i := do.New()
		do.Provide(i, func(do.Injector) (*Pillars, error) { return nil, boom })

		got, err := InvokePillars(i)
		test.Nil(t, got)
		test.ErrorIs(t, err, boom)
	})

	T.Run("a *Pillars provider that needs an unregistered dependency is an error", func(t *testing.T) {
		t.Parallel()

		// do reports the nested miss with the same sentinel as an absent
		// *Pillars. Reading the sentinel as absence would fall through to the
		// individual lookups, find nothing, and hand back a Pillars of nils
		// for a consumer that registered one — observability that looks
		// configured and exports nothing.
		i := do.New()
		do.Provide(i, func(i do.Injector) (*Pillars, error) {
			if _, err := do.Invoke[*unregisteredExporter](i); err != nil {
				return nil, err
			}

			return &Pillars{}, nil
		})

		got, err := InvokePillars(i)
		test.Nil(t, got)
		test.ErrorIs(t, err, do.ErrServiceNotFound)
	})

	T.Run("an individual pillar that needs an unregistered dependency is an error", func(t *testing.T) {
		t.Parallel()

		// The same line, one level down: with no *Pillars registered the
		// individual lookups run, and a logger whose provider cannot find its
		// sink is a failure rather than an absent logger.
		i := do.New()
		do.Provide(i, func(i do.Injector) (logging.Logger, error) {
			if _, err := do.Invoke[*unregisteredExporter](i); err != nil {
				return nil, err
			}

			return logging.EnsureLogger(nil), nil
		})

		got, err := InvokePillars(i)
		test.Nil(t, got)
		test.ErrorIs(t, err, do.ErrServiceNotFound)
	})
}

// unregisteredExporter stands in for a dependency a consumer's provider asks
// the container for and nothing registered.
type unregisteredExporter struct{}
