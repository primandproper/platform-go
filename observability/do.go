package observability

import (
	stderrors "errors"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingcfg "github.com/primandproper/platform-go/v13/observability/logging/config"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricscfg "github.com/primandproper/platform-go/v13/observability/metrics/config"
	"github.com/primandproper/platform-go/v13/observability/profiling"
	profilingcfg "github.com/primandproper/platform-go/v13/observability/profiling/config"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	tracingcfg "github.com/primandproper/platform-go/v13/observability/tracing/config"

	"github.com/samber/do/v2"
)

// InvokePillars assembles whatever observability the injector has been given,
// requiring none of it.
//
// A registered *Pillars wins outright; otherwise each pillar is looked up on
// its own and left nil when absent. Nil is the point — every constructor in
// this module resolves an absent dependency to its noop, so a service that
// registers no observability at all wires up and runs silently instead of
// panicking on a do.MustInvoke for a provider it never wanted.
//
// A service that *is* registered but fails to build is a different matter, and
// is returned as an error rather than quietly treated as absent: a metrics
// provider whose exporter cannot reach its collector should surface, not
// degrade to a noop that looks configured.
func InvokePillars(i do.Injector) (*Pillars, error) {
	if p, err := do.Invoke[*Pillars](i); err == nil {
		return p, nil
	} else if !stderrors.Is(err, do.ErrServiceNotFound) {
		return nil, errors.Wrap(err, "invoking observability pillars")
	}

	p := &Pillars{}

	logger, err := injection.InvokeOptional[logging.Logger](i)
	if err != nil {
		return nil, errors.Wrap(err, "invoking logger")
	}
	p.Logger = logger

	tracerProvider, err := injection.InvokeOptional[tracing.Provider](i)
	if err != nil {
		return nil, errors.Wrap(err, "invoking tracer provider")
	}
	p.TracerProvider = tracerProvider

	metricsProvider, err := injection.InvokeOptional[metrics.Provider](i)
	if err != nil {
		return nil, errors.Wrap(err, "invoking metrics provider")
	}
	p.MetricsProvider = metricsProvider

	profiler, err := injection.InvokeOptional[profiling.Provider](i)
	if err != nil {
		return nil, errors.Wrap(err, "invoking profiler")
	}
	p.Profiler = profiler

	return p, nil
}

// RegisterO11yConfigs registers sub-configs extracted from *Config with the injector.
// This extracts sub-configs from the parent *Config and registers them with the injector.
// Prerequisite: *Config must be registered in the injector before calling this.
func RegisterO11yConfigs(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*loggingcfg.Config, error) {
		cfg := do.MustInvoke[*Config](i)
		return &cfg.Logging, nil
	})
	do.Provide(i, func(i do.Injector) (*metricscfg.Config, error) {
		cfg := do.MustInvoke[*Config](i)
		return &cfg.Metrics, nil
	})
	do.Provide(i, func(i do.Injector) (*tracingcfg.Config, error) {
		cfg := do.MustInvoke[*Config](i)
		return &cfg.Tracing, nil
	})
	do.Provide(i, func(i do.Injector) (*profilingcfg.Config, error) {
		cfg := do.MustInvoke[*Config](i)
		return &cfg.Profiling, nil
	})
}
