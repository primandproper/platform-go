package searchsync

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/jobs"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// DefaultReindexBatchSize is how many documents a reindex scans and writes at a
// time when WithReindexBatchSize names no other number.
const DefaultReindexBatchSize = 500

// ReindexJobPrefix is prepended to the index name to form the jobs.Job name a
// Reindexer registers under. The name is the scheduler's lock key, so it is
// spelled in one place: two replicas that disagree about it both run the
// reindex.
const ReindexJobPrefix = "reindex-"

// ReindexResult is what a reindex did. It is returned even when the reindex
// fails partway, describing everything that landed before it stopped.
type ReindexResult struct {
	// Scanned is how many source documents the walk read.
	Scanned int64
	// Upserted is how many of them were written to the index. It trails
	// Scanned only when the reindex failed mid-batch.
	Upserted int64
	// Pruned is how many documents were deleted because the index held them
	// and the source no longer does. It is always zero without a pruner.
	Pruned int64
	// Batches is how many writes the walk made, upserts and deletes together.
	Batches int64
}

// Reindexer rebuilds an index from its source: for bootstrap, after a mapping
// change, or to repair drift the change feed missed.
//
// It owns no goroutines and no ticker. Register it with a jobs.Scheduler, whose
// distributed lock is what makes the rebuild run once across a fleet rather
// than once per replica — see Job.
type Reindexer[T any] struct {
	source Scanner[T]
	target Target[T]
	pruner Enumerator
	clock  clock.Clock
	o11y   observability.Observer

	documentsCounter metrics.Int64Counter
	prunedCounter    metrics.Int64Counter
	batchesCounter   metrics.Int64Counter
	failureCounter   metrics.Int64Counter
	reindexHist      metrics.Float64Histogram

	indexAttr metric.MeasurementOption

	name      string
	batchSize int
}

// NewReindexer builds a Reindexer over source and target.
//
// name identifies the index in spans, in metric attributes, and — prefixed with
// ReindexJobPrefix — as the scheduler lock key the rebuild runs under. It must
// be stable across deploys: renaming it during a rollout lets an old replica
// and a new one rebuild the same index at the same time.
//
// Without WithReindexPruner the rebuild is upsert-only. That is the right mode
// for a bootstrap and for a mapping change, and it cannot repair a document
// whose row is gone — nothing here can enumerate an index, so the walk over the
// index side has to be supplied.
func NewReindexer[T any](name string, source Scanner[T], target Target[T], opts ...ReindexOption) (*Reindexer[T], error) {
	if name == "" {
		return nil, ErrEmptyName
	}
	if source == nil {
		return nil, ErrNilSource
	}
	if target == nil {
		return nil, ErrNilTarget
	}

	o := newOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	r := &Reindexer[T]{
		name:      name,
		source:    source,
		target:    target,
		pruner:    o.pruner,
		clock:     o.clock,
		batchSize: o.batchSize,
		indexAttr: metric.WithAttributes(attribute.String(keys.IndexNameKey, name)),
	}

	r.o11y = observability.NewObserverWithValues(serviceName, o.logger, o.tracerProvider,
		map[string]any{keys.IndexNameKey: name})

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	var err error
	if r.documentsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_reindex_documents", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating reindex documents counter")
	}
	if r.prunedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_reindex_pruned", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating reindex pruned counter")
	}
	if r.batchesCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_reindex_batches", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating reindex batches counter")
	}
	if r.failureCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_reindex_failures", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating reindex failures counter")
	}
	if r.reindexHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_reindex_latency_ms", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating reindex latency histogram")
	}

	return r, nil
}

// Name returns the index name this Reindexer was built with.
func (r *Reindexer[T]) Name() string {
	return r.name
}

// Job returns the rebuild as a scheduled job, for registration with a
// jobs.Scheduler:
//
//	if err = scheduler.Register(reindexer.Job(jobs.MustCron("0 4 * * *"), time.Hour)); err != nil {
//	    return err
//	}
//
// leaseTTL must comfortably exceed how long the rebuild takes. The lease is not
// renewed while a job runs, so a rebuild that outlasts it lets a second replica
// start rebuilding the same index — wasteful rather than corrupting, since
// every write is idempotent, but it doubles the load on the source at the one
// moment the source is already under a full scan.
func (r *Reindexer[T]) Job(schedule jobs.Schedule, leaseTTL time.Duration) jobs.Job {
	return jobs.Job{
		Name:     ReindexJobPrefix + r.name,
		Schedule: schedule,
		LeaseTTL: leaseTTL,
		Run: func(ctx context.Context) error {
			_, err := r.Reindex(ctx)

			return err
		},
	}
}

// Reindex walks the source and writes every document to the index, pruning the
// documents the index holds and the source does not when a pruner was supplied.
//
// A failed batch stops the walk and returns what landed before it, rather than
// skipping ahead. There is nothing to salvage by continuing: the walk is a
// keyset scan that has to resume from somewhere, the next scheduled rebuild
// starts over from the beginning anyway, and every write it repeats is
// idempotent. A half-finished rebuild leaves the index in exactly the state a
// half-finished rebuild should — closer to the source than it was.
func (r *Reindexer[T]) Reindex(ctx context.Context) (*ReindexResult, error) {
	startTime := r.clock.Now()

	ctx, op := r.o11y.Begin(ctx,
		observability.WithValue(batchSizeKey, r.batchSize),
		observability.WithValue(pruningKey, r.pruner != nil))
	defer op.End()

	defer func() {
		r.reindexHist.Record(ctx, float64(r.clock.Since(startTime).Milliseconds()), r.indexAttr)
	}()

	result, err := r.walk(ctx)

	op.SetValues(map[string]any{
		scannedKey:  result.Scanned,
		upsertedKey: result.Upserted,
		prunedKey:   result.Pruned,
		batchesKey:  result.Batches,
	})

	if err != nil {
		r.failureCounter.Add(ctx, 1, r.indexAttr)

		return result, op.Error(err, "reindexing search index %q", r.name)
	}

	op.Logger().Info("search index reindexed")

	return result, nil
}

// walk is the rebuild itself. Both modes share the batching and the accounting;
// they differ only in whether there is a second stream to merge against.
func (r *Reindexer[T]) walk(ctx context.Context) (*ReindexResult, error) {
	result := &ReindexResult{}

	source := &cursor[Document[T]]{
		page:  r.source.Scan,
		idOf:  func(doc Document[T]) string { return doc.ID },
		limit: r.batchSize,
	}

	upserts := make([]Document[T], 0, r.batchSize)
	flushUpserts := func() error {
		if len(upserts) == 0 {
			return nil
		}

		if err := r.target.Upsert(ctx, upserts...); err != nil {
			return platformerrors.Wrap(err, "upserting reindex batch")
		}

		r.record(ctx, result, int64(len(upserts)), 0)
		upserts = upserts[:0]

		return nil
	}

	deletes := make([]string, 0, r.batchSize)
	flushDeletes := func() error {
		if len(deletes) == 0 {
			return nil
		}

		if err := r.target.Delete(ctx, deletes...); err != nil {
			return platformerrors.Wrap(err, "pruning reindex batch")
		}

		r.record(ctx, result, 0, int64(len(deletes)))
		deletes = deletes[:0]

		return nil
	}

	// Upsert-only. There is no second stream, so there is nothing to merge and
	// nothing that could be inferred to be stale.
	if r.pruner == nil {
		for {
			doc, _, ok, err := source.next(ctx)
			if err != nil {
				return result, err
			}
			if !ok {
				return result, flushUpserts()
			}

			result.Scanned++
			upserts = append(upserts, doc)

			if len(upserts) >= r.batchSize {
				if err = flushUpserts(); err != nil {
					return result, err
				}
			}
		}
	}

	// Merge two streams ordered by the same key. A source ID the index has not
	// reached is a document to write; an index ID the source has passed is a
	// document whose row is gone. Both streams are checked for ascending byte
	// order as they are read — if either is in a different order, that second
	// inference is wrong and would delete live documents, so it aborts instead.
	index := &cursor[string]{
		page:  r.pruner.Scan,
		idOf:  func(id string) string { return id },
		limit: r.batchSize,
	}

	srcDoc, srcID, srcOK, err := source.next(ctx)
	if err != nil {
		return result, err
	}

	_, idxID, idxOK, err := index.next(ctx)
	if err != nil {
		return result, err
	}

	for srcOK || idxOK {
		switch {
		case idxOK && (!srcOK || idxID < srcID):
			deletes = append(deletes, idxID)

			if _, idxID, idxOK, err = index.next(ctx); err != nil {
				return result, err
			}
		default:
			// The source is at or before the index. Either the index does not
			// hold this document yet or it holds an older copy; both want the
			// current one written.
			result.Scanned++
			upserts = append(upserts, srcDoc)

			if srcOK && idxOK && srcID == idxID {
				if _, idxID, idxOK, err = index.next(ctx); err != nil {
					return result, err
				}
			}

			if srcDoc, srcID, srcOK, err = source.next(ctx); err != nil {
				return result, err
			}
		}

		if len(upserts) >= r.batchSize {
			if err = flushUpserts(); err != nil {
				return result, err
			}
		}

		if len(deletes) >= r.batchSize {
			if err = flushDeletes(); err != nil {
				return result, err
			}
		}
	}

	if err = flushUpserts(); err != nil {
		return result, err
	}

	return result, flushDeletes()
}

// record accounts for one write, on the result and on the instruments together
// so the two cannot disagree.
func (r *Reindexer[T]) record(ctx context.Context, result *ReindexResult, upserted, pruned int64) {
	result.Batches++
	r.batchesCounter.Add(ctx, 1, r.indexAttr)

	if upserted > 0 {
		result.Upserted += upserted
		r.documentsCounter.Add(ctx, upserted, r.indexAttr)
	}

	if pruned > 0 {
		result.Pruned += pruned
		r.prunedCounter.Add(ctx, pruned, r.indexAttr)
	}
}

// cursor pages one ordered stream — the source's documents or the index's IDs —
// and enforces the ordering both of them promise.
//
// It is generic over the element rather than written twice because the checking
// is the part worth having in one place: an ID that repeats, goes backwards, or
// is empty is the difference between a reindex that converges and one that
// deletes live documents, and it must be caught identically on both sides.
type cursor[E any] struct {
	page  func(ctx context.Context, after string, limit int) ([]E, error)
	idOf  func(E) string
	after string
	buf   []E
	pos   int
	limit int

	// drained records that the last page came back short, which is how both
	// contracts say the walk is over. Without it a stream whose length is an
	// exact multiple of the batch size would need one extra empty page.
	drained bool
}

// next returns the following element and its ID, reporting ok=false at the end
// of the stream.
func (c *cursor[E]) next(ctx context.Context) (element E, id string, ok bool, err error) {
	var zero E

	for c.pos >= len(c.buf) {
		if c.drained {
			return zero, "", false, nil
		}

		var page []E
		if page, err = c.page(ctx, c.after, c.limit); err != nil {
			return zero, "", false, platformerrors.Wrapf(err, "scanning after %q", c.after)
		}

		if len(page) < c.limit {
			c.drained = true
		}

		if len(page) == 0 {
			return zero, "", false, nil
		}

		// The whole page is checked before any of it is handed out, so a
		// disordered page cannot be half-applied.
		previous := c.after
		for i := range page {
			pageID := c.idOf(page[i])

			if pageID == "" {
				return zero, "", false, ErrEmptyDocumentID
			}

			if pageID <= previous {
				return zero, "", false, platformerrors.Wrapf(ErrUnsortedScan, "%q followed %q", pageID, previous)
			}

			previous = pageID
		}

		c.buf, c.pos, c.after = page, 0, previous
	}

	element = c.buf[c.pos]
	c.pos++

	return element, c.idOf(element), true, nil
}
