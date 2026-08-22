package healthcheck

import (
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures a CheckerRegistry. The zero configuration works: an absent
// logger logs nowhere, an absent tracer provider traces nowhere, and an absent
// metrics provider records nothing.
//
// Worth setting, though — all three of them. This package's entire job is
// noticing that something is wrong, and a registry with no observability
// notices it into a JSON body that only whatever polled the probe ever reads.
type Option func(*options)

type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithLogger attaches a logger, which is where a component going down or coming
// back is reported.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, giving each check its own span
// under the run that started it.
//
// A service that serves its probes from this module's HTTP server should not
// expect to see them: the probe paths are span-filtered there, deliberately, and
// a child span inherits its parent's sampling decision. The transitions still
// log and count, which is what an operator is alerting on anyway.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the transition counter and
// the down-component gauge.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithPillars supplies logger, tracer provider, and metrics provider at once.
//
// Options apply in order, so a WithPillars followed by a narrower option wins
// for that component: WithPillars(p) then WithMetricsProvider(nil) leaves the
// registry traced and logged but unmetered.
func WithPillars(pillars *observability.Pillars) Option {
	return func(o *options) {
		o.logger, o.tracerProvider, o.metricsProvider = pillars.Deps()
	}
}
