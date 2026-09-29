package passkeyscfg

import (
	"github.com/primandproper/platform-go/v14/authentication/passkeys"

	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures how this package's constructors assemble what they build.
// One Option type serves both, and each hands the observability dependencies
// to its own component.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	store   []passkeys.SQLStoreOption
	service []passkeys.ServiceOption
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

// WithTracerProvider attaches a tracer provider. An absent one traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent one records
// nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithPillars attaches a logger, tracer provider and metrics provider at once.
// A nil Pillars attaches nothing, and later options override it.
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, o.metricsProvider = p.Deps() }
}

// WithStoreOptions passes opts to NewStore, which applies them after the ones
// it derives from configuration.
func WithStoreOptions(opts ...passkeys.SQLStoreOption) Option {
	return func(o *options) { o.store = append(o.store, opts...) }
}

// WithServiceOptions passes opts to NewService, which applies them after the
// ones it derives from its arguments — so a caller can attach a username
// resolver, hooks or the last-credential guard's answer about passwords.
func WithServiceOptions(opts ...passkeys.ServiceOption) Option {
	return func(o *options) { o.service = append(o.service, opts...) }
}
