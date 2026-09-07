package identitycfg

import (
	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/observability"
	"github.com/primandproper/platform-go/v14/observability/logging"
	"github.com/primandproper/platform-go/v14/observability/metrics"
	"github.com/primandproper/platform-go/v14/observability/tracing"
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

	hooks   identity.Hooks
	store   []identity.SQLStoreOption
	service []identity.ServiceOption
	server  []identitygrpc.Option
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
// prefix and the clock included.
func WithStoreOptions(opts ...identity.SQLStoreOption) Option {
	return func(o *options) { o.store = append(o.store, opts...) }
}

// WithHooks supplies what commits alongside each of the Service's operations —
// the audit entry, the outbox row, the search stamp.
//
// It is an option rather than a parameter because it is the one dependency a
// consumer legitimately has none of: identity.NoopHooks is the default, and an
// application with nothing to commit beside an identity write configures
// nothing. A nil Hooks is ignored rather than installed, since installing one
// would panic on the first operation.
func WithHooks(hooks identity.Hooks) Option {
	return func(o *options) {
		if hooks != nil {
			o.hooks = hooks
		}
	}
}

// WithServiceOptions passes opts to NewService, after the options it derives
// from configuration.
func WithServiceOptions(opts ...identity.ServiceOption) Option {
	return func(o *options) { o.service = append(o.service, opts...) }
}

// WithServerOptions passes opts to NewServer, after the options it derives from
// configuration — so a caller overrides the invitation TTL or supplies a token
// minter of their own here.
func WithServerOptions(opts ...identitygrpc.Option) Option {
	return func(o *options) { o.server = append(o.server, opts...) }
}
