package rbaccfg

import (
	"github.com/primandproper/platform-go/v14/rbac"

	authorizationcfg "github.com/primandproper/primitives-go/v2/authorization/config"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures how NewPolicyResolver assembles its resolver.
//
// The passthroughs each apply only when the thing they name is built, so one
// wiring site can carry options for whichever provider a given deployment turns
// out to run. They are appended after the options this package derives from its
// arguments, so a caller can override what it would otherwise be given.
type Option func(*options)

// options collects the passthroughs, plus the observability the resolver is
// built with.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	database []rbac.Option
	resolver []authorizationcfg.Option
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

// resolverOptions renders what this package was given as options for the half
// it delegates to, with the caller's own passthroughs last so they win.
func (o *options) resolverOptions() []authorizationcfg.Option {
	return append([]authorizationcfg.Option{
		authorizationcfg.WithLogger(o.logger),
		authorizationcfg.WithTracerProvider(o.tracerProvider),
		authorizationcfg.WithMetricsProvider(o.metricsProvider),
	}, o.resolver...)
}

// WithDatabaseOptions passes opts to the database resolver, when the database
// provider is selected.
func WithDatabaseOptions(opts ...rbac.Option) Option {
	return func(o *options) { o.database = append(o.database, opts...) }
}

// WithResolverOptions passes opts to authorizationcfg, which builds the static
// resolver and applies the caching decorator to both providers. It is how
// authorizationcfg.WithStaticOptions and authorizationcfg.WithCachedOptions
// reach the half that owns them.
//
// Go allows one variadic per function and that slot belongs to this package's
// own Option, so the primitive half's options arrive nested rather than
// mirrored under names of their own: a mirror would be a second place for each
// of them to be spelled, and would have to grow every time the other half does.
func WithResolverOptions(opts ...authorizationcfg.Option) Option {
	return func(o *options) { o.resolver = append(o.resolver, opts...) }
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on policy
// resolution. An absent tracer provider traces nowhere.
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
