package syncsource

import (
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	searchsync "github.com/primandproper/platform-go/v13/search/sync"
)

// Option configures what NewSyncer and NewReindexer build. The zero
// configuration works: an absent logger logs nowhere, an absent tracer provider
// traces nowhere, and an absent metrics provider records nothing.
//
// It is one type for both constructors rather than two, because the pillars are
// the same three things either way and a wiring site that builds both from one
// Source passes the same options to each.
//
// Option is not parameterized on the Source's type arguments even though the
// constructors it configures are. Go cannot infer a type argument from a call's
// result type, so a WithLogger[Order, OrderDoc](logger) would have to be spelled
// out at every call site, forever, to configure something that does not depend
// on either type.
type Option func(*options)

type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	syncerOptions   []searchsync.SyncerOption
	reindexOptions  []searchsync.ReindexOption
}

func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling a span per applied
// event and per rebuild.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. Without one there is no lag
// histogram, which is the one instrument that distinguishes a working sync from
// a stopped one — see the searchsync package documentation.
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

// WithSyncerOptions passes options through to the searchsync.Syncer that
// NewSyncer builds — WithSyncerClock, and anything added there later.
//
// The pillars are not among them: they arrive as this package's own options, so
// one set of them configures whichever of the two things a wiring site builds.
// NewReindexer ignores these.
func WithSyncerOptions(opts ...searchsync.SyncerOption) Option {
	return func(o *options) { o.syncerOptions = append(o.syncerOptions, opts...) }
}

// WithReindexOptions passes options through to the searchsync.Reindexer that
// NewReindexer builds — WithReindexBatchSize and WithReindexPruner in
// particular. NewSyncer ignores these.
//
// Pruning is worth a thought rather than a default. Without a pruner a rebuild
// converges the documents the source has and leaves behind any the source no
// longer names; deletions still reach the index through the change feed, which
// is where they are timely anyway. With one, a rebuild also repairs the
// documents a missed delete stranded — and nothing behind textsearch.Index can
// enumerate an index, so the Enumerator has to come from the application.
func WithReindexOptions(opts ...searchsync.ReindexOption) Option {
	return func(o *options) { o.reindexOptions = append(o.reindexOptions, opts...) }
}
