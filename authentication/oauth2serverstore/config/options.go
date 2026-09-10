package oauth2serverstorecfg

import (
	"github.com/primandproper/platform-go/v14/authentication/oauth2serverstore"

	oauth2servercfg "github.com/primandproper/primitives-go/authentication/oauth2server/config"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
)

// Option configures how NewStore and NewServer assemble their pieces.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing.
type Option func(*options)

// options collects what the options set. The pass-through slices exist because
// Go allows one variadic per function and that slot belongs to this package's
// own Option; anything bound for a component this constructor builds arrives
// through a WithXOptions instead.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	databaseStore []oauth2serverstore.Option
	server        []oauth2servercfg.Option
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

// serverOptions renders what this package was given as options for the half it
// delegates to, with the caller's own passthroughs last so they win.
func (o *options) serverOptions() []oauth2servercfg.Option {
	return append([]oauth2servercfg.Option{
		oauth2servercfg.WithLogger(o.logger),
		oauth2servercfg.WithTracerProvider(o.tracerProvider),
		oauth2servercfg.WithMetricsProvider(o.metricsProvider),
	}, o.server...)
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider. An absent tracer provider
// traces nowhere.
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
// its pillars and then override one of them:
//
//	oauth2serverstorecfg.NewServer(ctx, cfg, db, authenticator,
//		oauth2serverstorecfg.WithPillars(pillars),
//		oauth2serverstorecfg.WithMetricsProvider(nil), // this server stays unmetered
//	)
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, o.metricsProvider = p.Deps() }
}

// WithDatabaseStoreOptions passes options through to the database store — the
// clock, most usefully. They are ignored under any other provider.
func WithDatabaseStoreOptions(opts ...oauth2serverstore.Option) Option {
	return func(o *options) { o.databaseStore = append(o.databaseStore, opts...) }
}

// WithServerOptions passes opts to oauth2servercfg, which builds the server and
// the memory store. It is how oauth2servercfg.WithServerOptions and
// oauth2servercfg.WithMemoryStoreOptions reach the half that owns them — and so
// how the login renderer, the registration policy and the subject resolver
// reach the server.
//
// Go allows one variadic per function and that slot belongs to this package's
// own Option, so the primitive half's options arrive nested rather than
// mirrored under names of their own: a mirror would be a second place for each
// of them to be spelled, and would have to grow every time the other half does.
func WithServerOptions(opts ...oauth2servercfg.Option) Option {
	return func(o *options) { o.server = append(o.server, opts...) }
}
