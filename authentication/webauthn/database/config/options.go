package webauthndbcfg

import (
	webauthndatabase "github.com/primandproper/platform-go/v14/authentication/webauthn/database"

	webauthncfg "github.com/primandproper/primitives-go/authentication/webauthn/config"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
)

// Option configures how NewSessionStore and NewRelyingParty assemble their
// pieces.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. Requiring them positionally would make a caller who wants none of
// the three name all three anyway, usually as noops.
type Option func(*options)

// options collects what the options set. The pass-through slices exist because
// Go allows one variadic per function and that slot belongs to this package's
// own Option; anything bound for a component this constructor builds arrives
// through a WithXOptions instead.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	databaseStore []webauthndatabase.Option
	relyingParty  []webauthncfg.Option
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

// relyingPartyOptions renders what this package was given as options for the
// half it delegates to, with the caller's own passthroughs last so they win.
func (o *options) relyingPartyOptions() []webauthncfg.Option {
	return append([]webauthncfg.Option{
		webauthncfg.WithLogger(o.logger),
		webauthncfg.WithTracerProvider(o.tracerProvider),
		webauthncfg.WithMetricsProvider(o.metricsProvider),
	}, o.relyingParty...)
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every
// ceremony step. An absent tracer provider traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the ceremony instruments
// and the sweeper's counters. An absent provider records nothing.
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
//	webauthndbcfg.NewRelyingParty(ctx, cfg, db,
//	    webauthndbcfg.WithPillars(pillars),
//	    webauthndbcfg.WithMetricsProvider(nil), // these ceremonies stay unmetered
//	)
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, o.metricsProvider = p.Deps() }
}

// WithDatabaseStoreOptions passes options through to the database store —
// WithCodec and WithClock, most usefully. They are ignored under any other
// provider.
func WithDatabaseStoreOptions(opts ...webauthndatabase.Option) Option {
	return func(o *options) { o.databaseStore = append(o.databaseStore, opts...) }
}

// WithRelyingPartyOptions passes opts to webauthncfg, which builds the relying
// party and the cache-backed store. It is how
// webauthncfg.WithRelyingPartyOptions and webauthncfg.WithCacheStoreOptions
// reach the half that owns them.
//
// Go allows one variadic per function and that slot belongs to this package's
// own Option, so the primitive half's options arrive nested rather than
// mirrored under names of their own: a mirror would be a second place for each
// of them to be spelled, and would have to grow every time the other half does.
func WithRelyingPartyOptions(opts ...webauthncfg.Option) Option {
	return func(o *options) { o.relyingParty = append(o.relyingParty, opts...) }
}
