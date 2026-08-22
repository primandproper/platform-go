package timerscfg

import (
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/timers"
)

// Option configures how this package's constructors assemble what they build.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. Requiring them positionally made a caller that wanted none of the
// three name all three anyway, usually as noops.
//
// WithTimerOptions passes options through to the set itself. It cannot be a
// second variadic on the constructors: Go allows one per function, and that slot
// is what makes the observability optional.
type Option func(*options)

// options collects what the options set.
type options struct {
	clock           clock.Clock
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	timers []timers.Option
}

// newOptions applies opts, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithClock attaches a clock. An absent clock means the wall clock, which inside
// a testing/synctest bubble already reads bubble time.
func WithClock(c clock.Clock) Option {
	return func(o *options) { o.clock = c }
}

// WithLogger attaches a logger. An absent logger logs nowhere — including the
// worker's report of a handler that has been failing every pass, which has no
// caller to be returned to.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on the
// instrumented operations. An absent tracer provider traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing — including the lateness gauge, which is the only way to see that a
// fleet has stopped firing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithPillars attaches a logger, tracer provider, and metrics provider in one
// go, for the common case where a caller has already built them together. A nil
// Pillars attaches nothing.
//
// It is applied in order with the individual options, so a caller can hand over
// its pillars and then override one of them.
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, o.metricsProvider = p.Deps() }
}

// WithTimerOptions passes opts to the set, which applies them after the options
// derived from configuration — so a caller can override anything. It is how a
// custom key codec or a wakeup channel reaches a set built from configuration,
// since both are Go values the environment cannot name.
func WithTimerOptions(opts ...timers.Option) Option {
	return func(o *options) { o.timers = append(o.timers, opts...) }
}
