package oauth2servercfg

import (
	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	oauth2database "github.com/primandproper/platform-go/v13/authentication/oauth2server/database"
	oauth2memory "github.com/primandproper/platform-go/v13/authentication/oauth2server/memory"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures how NewStore and NewServer assemble their pieces.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, an absent metrics provider records
// nothing. Requiring them positionally would make a caller who wants none of
// the three name all three anyway, usually as noops.
type Option func(*options)

// options collects what the options set. The three pass-through slices exist
// because Go allows one variadic per function and that slot belongs to this
// package's own Option; anything bound for a component this constructor builds
// arrives through a WithXOptions instead.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	server        []oauth2server.Option
	memoryStore   []oauth2memory.Option
	databaseStore []oauth2database.Option
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
// endpoint and every store operation. An absent one traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the server's counters and
// latency histogram, and for the store's sweep counters. An absent one records
// nothing.
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
//	oauth2servercfg.NewServer(ctx, cfg, db, authenticator,
//		oauth2servercfg.WithPillars(pillars),
//		oauth2servercfg.WithMetricsProvider(nil), // this server stays unmetered
//	)
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, o.metricsProvider = p.Deps() }
}

// WithServerOptions passes options through to the Server this builds. They are
// applied after the ones derived from the Config, so they win.
//
// It is how the login form is replaced — oauth2server.WithLoginRenderer — how a
// deployment supplies its own RegistrationPolicy, and how it registers an
// oauth2server.SubjectResolver so that clients holding a session rather than a
// keyboard skip the form. None of the three is expressible as an environment
// variable, and none has a defensible default that could be named by one.
func WithServerOptions(opts ...oauth2server.Option) Option {
	return func(o *options) { o.server = append(o.server, opts...) }
}

// WithMemoryStoreOptions passes options through to the memory store. They are
// ignored under any other provider.
func WithMemoryStoreOptions(opts ...oauth2memory.Option) Option {
	return func(o *options) { o.memoryStore = append(o.memoryStore, opts...) }
}

// WithDatabaseStoreOptions passes options through to the database store —
// WithClock, most usefully. They are ignored under any other provider.
func WithDatabaseStoreOptions(opts ...oauth2database.Option) Option {
	return func(o *options) { o.databaseStore = append(o.databaseStore, opts...) }
}
