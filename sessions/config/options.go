package sessionscfg

import (
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/sessions"
	sessionscache "github.com/primandproper/platform-go/v13/sessions/cache"
	sessionsdatabase "github.com/primandproper/platform-go/v13/sessions/database"
	sessionshttp "github.com/primandproper/platform-go/v13/sessions/http"
)

// Option configures how NewStore and NewManager assemble their pieces.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. Requiring them positionally would make a caller who wants none of
// the three name all three anyway, usually as noops.
//
// It carries no type parameter even though the constructors do. Go cannot infer
// a type argument from a call's result type, so an Option[T] would force every
// call site to spell the payload type out by hand — WithLogger[Principal](l) —
// forever.
type Option func(*options)

// options collects what the options set. The four pass-through slices exist
// because Go allows one variadic per function and that slot belongs to this
// package's own Option; anything bound for a component this constructor builds
// arrives through a WithXOptions instead.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	store           []sessions.Option
	manager         []sessionshttp.Option
	cacheBackend    []sessionscache.Option
	databaseBackend []sessionsdatabase.Option
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

// WithTracerProvider attaches a tracer provider, enabling spans on every
// session operation. An absent tracer provider traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the store's counters and
// latency histogram. An absent provider records nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithPillars attaches a logger, tracer provider, and metrics provider in one
// go, for the common case where a caller has already built them together. A nil
// Pillars attaches nothing.
//
// It is applied in order with the individual options, so a caller can hand over
// its pillars and then override one of them:
//
//	sessionscfg.NewStore[Principal](ctx, cfg, nil,
//		sessionscfg.WithPillars(pillars),
//		sessionscfg.WithMetricsProvider(nil), // this store stays unmetered
//	)
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, o.metricsProvider = p.Deps() }
}

// WithStoreOptions passes options through to the sessions.Store this builds.
// They are applied after the ones derived from the Config, so they win.
func WithStoreOptions(opts ...sessions.Option) Option {
	return func(o *options) { o.store = append(o.store, opts...) }
}

// WithManagerOptions passes options through to the sessions/http Manager that
// NewManager builds. They are applied after the ones derived from the Config,
// so they win. NewStore ignores them.
func WithManagerOptions(opts ...sessionshttp.Option) Option {
	return func(o *options) { o.manager = append(o.manager, opts...) }
}

// WithCacheBackendOptions passes options through to the cache backend. They are
// ignored under any other provider.
func WithCacheBackendOptions(opts ...sessionscache.Option) Option {
	return func(o *options) { o.cacheBackend = append(o.cacheBackend, opts...) }
}

// WithDatabaseBackendOptions passes options through to the database backend —
// WithCodec, most usefully. They are ignored under any other provider.
func WithDatabaseBackendOptions(opts ...sessionsdatabase.Option) Option {
	return func(o *options) { o.databaseBackend = append(o.databaseBackend, opts...) }
}
