package memory

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures an in-memory cache at construction. Options are applied in
// the order given, and a nil option is ignored.
//
// It carries no type parameter even though the cache does: almost nothing an
// Option sets depends on the cached type, and Go cannot infer a type argument
// from a call's result type — so an Option[T] would force every call site to
// spell the cached type out by hand — WithLogger[MyValue](l) — forever.
// WithLoader is the one option that depends on the cached type; it stays
// generic but still needs no annotation, because T is inferable from the loader
// it is handed.
type Option func(*options)

// options accumulates what the options set, so Option can stay free of the
// cache's type parameter.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	// janitorCtx and janitorInterval are held rather than started, because a
	// janitor must not observe a half-built cache; the constructor launches the
	// sweep once everything else is in place.
	janitorCtx context.Context //nolint:containedctx // deliberate: see WithJanitor

	// loader holds a Loader[T] for the T of the cache being built. It is typed
	// as any because Option cannot name T; NewInMemoryCache asserts it back to
	// the concrete type and reports a mismatch rather than ignoring it.
	loader any

	janitorInterval time.Duration

	maxEntries     int
	evictionPolicy EvictionPolicy
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every cache
// operation. An absent tracer provider traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the cache's hit, miss,
// set, delete, eviction, and load counters and its latency histogram. An absent
// provider records nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}
