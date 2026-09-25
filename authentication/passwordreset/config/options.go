package passwordresetcfg

import (
	"github.com/primandproper/platform-go/v14/authentication/passwordreset"

	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures how this package's constructors assemble what they build.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. One Option type serves both constructors, and each hands the three
// to its own component — the leaf package names its store and service options
// apart, and this is the one place a caller wiring both wants them together.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	store   []passwordreset.Option
	service []passwordreset.ServiceOption
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

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on the
// instrumented operations. An absent tracer provider traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing.
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

// WithStoreOptions passes opts to NewStore, which applies them after the options
// it derives from configuration — so a caller can override anything, the
// sweeper included.
func WithStoreOptions(opts ...passwordreset.Option) Option {
	return func(o *options) { o.store = append(o.store, opts...) }
}

// WithServiceOptions passes opts to NewService, which applies them after the
// options it derives from configuration — so a caller can override the token
// lifetime or the request floor, or replace the flow's clock.
func WithServiceOptions(opts ...passwordreset.ServiceOption) Option {
	return func(o *options) { o.service = append(o.service, opts...) }
}
