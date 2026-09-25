package oauth2clientscfg

import (
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"

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
// nothing. Requiring them positionally made a caller that wanted none of the
// three name all three anyway, usually as noops.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	hooks   oauth2clients.Hooks
	store   []oauth2clients.SQLStoreOption
	service []oauth2clients.ServiceOption
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
// it derives from configuration — so a caller can override anything, the table
// prefix included.
func WithStoreOptions(opts ...oauth2clients.SQLStoreOption) Option {
	return func(o *options) { o.store = append(o.store, opts...) }
}

// WithHooks supplies what commits alongside each of the Service's operations —
// the audit entry, the outbox row.
//
// It is an option rather than a parameter because it is the one dependency a
// consumer legitimately has none of: oauth2clients.NoopHooks is the default,
// and an application with nothing to commit beside a registration configures
// nothing. A nil Hooks is ignored rather than installed, since installing one
// would panic on the first operation.
func WithHooks(hooks oauth2clients.Hooks) Option {
	return func(o *options) {
		if hooks != nil {
			o.hooks = hooks
		}
	}
}

// WithServiceOptions passes opts to NewService, after the options it derives
// from configuration — so a caller supplies a credential generator of their own
// here.
func WithServiceOptions(opts ...oauth2clients.ServiceOption) Option {
	return func(o *options) { o.service = append(o.service, opts...) }
}
