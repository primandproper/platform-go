package webauthncfg

import (
	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	webauthncache "github.com/primandproper/platform-go/v13/authentication/webauthn/cache"
	webauthndatabase "github.com/primandproper/platform-go/v13/authentication/webauthn/database"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
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

// options collects what the options set. The three pass-through slices exist
// because Go allows one variadic per function and that slot belongs to this
// package's own Option; anything bound for a component this constructor builds
// arrives through a WithXOptions instead.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	relyingParty  []webauthn.Option
	databaseStore []webauthndatabase.Option
	cacheStore    []webauthncache.Option
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
//	webauthncfg.NewRelyingParty(ctx, cfg, db,
//		webauthncfg.WithPillars(pillars),
//		webauthncfg.WithMetricsProvider(nil), // these ceremonies stay unmetered
//	)
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, o.metricsProvider = p.Deps() }
}

// WithRelyingPartyOptions passes options through to the webauthn.RelyingParty
// this builds. They are applied after the ones derived from the Config, so they
// win. NewSessionStore ignores them.
func WithRelyingPartyOptions(opts ...webauthn.Option) Option {
	return func(o *options) { o.relyingParty = append(o.relyingParty, opts...) }
}

// WithDatabaseStoreOptions passes options through to the database store —
// WithCodec and WithClock, most usefully. They are ignored under any other
// provider.
func WithDatabaseStoreOptions(opts ...webauthndatabase.Option) Option {
	return func(o *options) { o.databaseStore = append(o.databaseStore, opts...) }
}

// WithCacheStoreOptions passes options through to the cache store. They are
// ignored under any other provider.
func WithCacheStoreOptions(opts ...webauthncache.Option) Option {
	return func(o *options) { o.cacheStore = append(o.cacheStore, opts...) }
}
