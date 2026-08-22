package searchsync

import (
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// options accumulates what both option types set, so neither has to carry the
// type parameter of the thing it configures.
//
// Neither SyncerOption nor ReindexOption is parameterized on T, even though
// both configure a generic type. Go cannot infer a type argument from a call's
// result type, so a SyncerOption[T] would force every call site to spell the
// document type out by hand — WithSyncerLogger[OrderDoc](logger) — forever.
// Nothing either option carries depends on T, so nothing is lost.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	clock           clock.Clock
	pruner          Enumerator
	stamper         Stamper
	batchSize       int
}

func newOptions() *options {
	return &options{
		clock:     clock.NewClock(),
		batchSize: DefaultReindexBatchSize,
	}
}

// SyncerOption configures a Syncer. The zero configuration works: an absent
// logger logs nowhere, an absent tracer provider traces nowhere, and an absent
// metrics provider records nothing.
type SyncerOption func(*options)

// WithSyncerLogger attaches a logger.
func WithSyncerLogger(logger logging.Logger) SyncerOption {
	return func(o *options) { o.logger = logger }
}

// WithSyncerTracerProvider attaches a tracer provider, enabling a span per
// applied event.
func WithSyncerTracerProvider(tracerProvider tracing.Provider) SyncerOption {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithSyncerMetricsProvider attaches a metrics provider. Without one there is
// no lag histogram, which is the one instrument that distinguishes a working
// sync from a stopped one — see the package documentation.
func WithSyncerMetricsProvider(metricsProvider metrics.Provider) SyncerOption {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithSyncerClock swaps the clock the lag is measured against. Tests generally
// do not need it: under testing/synctest the default clock already runs on
// bubble time.
func WithSyncerClock(c clock.Clock) SyncerOption {
	return func(o *options) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithSyncerStamper supplies what a Syncer tells about the documents an index
// accepted, so that last_indexed_at on the rows behind them can be maintained.
//
// The column is a convention this module already derives from — querygen treats
// its presence as what marks a table as one search/sync mirrors, forbids any
// caller from supplying it, and emits the reindex scan that reads it. This is
// the writer that convention names. Without one the Syncer stamps nothing,
// which is the right behavior for an index whose source table does not carry
// the column at all.
//
// Pass a Buffer from NewStampBuffer rather than a direct writer. One UPDATE per
// applied document from every worker of a jobs.Pool at once is how a stamping
// write deadlocks against itself; the reason, and what the Buffer does about
// it, are in NewStampBuffer.
//
// A Syncer stamps only the documents the index actually took. A delete stamps
// nothing, because there is no document left to record having indexed; an
// upsert whose row has since vanished is applied as a delete and stamps nothing
// either; and a failed write stamps nothing, since the whole value of the
// column is that it says what the index holds rather than what was attempted.
//
// There is no reindex counterpart, and that is a decision rather than an
// omission. A Reindexer writes every document there is, so stamping it would
// make the column a record of when the last rebuild ran — the same value on
// every row — rather than of how current each document is, which is the reading
// the reindex scan itself depends on.
func WithSyncerStamper(stamper Stamper) SyncerOption {
	return func(o *options) {
		if stamper != nil {
			o.stamper = stamper
		}
	}
}

// ReindexOption configures a Reindexer.
type ReindexOption func(*options)

// WithReindexLogger attaches a logger.
func WithReindexLogger(logger logging.Logger) ReindexOption {
	return func(o *options) { o.logger = logger }
}

// WithReindexTracerProvider attaches a tracer provider, enabling a span per
// reindex and per batch.
func WithReindexTracerProvider(tracerProvider tracing.Provider) ReindexOption {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithReindexMetricsProvider attaches a metrics provider.
func WithReindexMetricsProvider(metricsProvider metrics.Provider) ReindexOption {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithReindexBatchSize sets how many documents are scanned and written at a
// time. A non-positive size is ignored, leaving DefaultReindexBatchSize.
func WithReindexBatchSize(n int) ReindexOption {
	return func(o *options) {
		if n > 0 {
			o.batchSize = n
		}
	}
}

// WithReindexPruner supplies the index-side enumeration that lets a reindex
// delete documents whose source rows are gone.
//
// Without one a reindex is upsert-only: it converges the index toward the
// source and never removes anything, which is right for a bootstrap into an
// empty index and for a mapping change, and not enough for drift repair. The
// two modes are named rather than one being a degraded version of the other —
// a reindexer reports which one it is on its spans, and the package
// documentation says what each is for.
//
// A nil Enumerator is ignored rather than treated as pruning with nothing to
// prune, which would delete the entire index.
func WithReindexPruner(pruner Enumerator) ReindexOption {
	return func(o *options) {
		if pruner != nil {
			o.pruner = pruner
		}
	}
}
