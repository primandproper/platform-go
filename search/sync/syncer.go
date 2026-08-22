package searchsync

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/retry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Syncer applies index change events to one index.
//
// It owns no goroutines and reads from no queue. Handle is a jobs.Handler, so
// the consumption, concurrency, retry, dead-lettering and panic containment
// around it all come from jobs.Pool, which already does those four things
// carefully — see the package documentation for the wiring.
//
// That holds with a Stamper too: the buffering behind one, and the goroutine
// that flushes it, belong to the caller who built it.
type Syncer[T any] struct {
	source  Fetcher[T]
	target  Target[T]
	stamper Stamper
	clock   clock.Clock
	o11y    observability.Observer

	// unmarshaler is pinned to JSON rather than configurable, because the
	// outbox's marshaler is: it encodes a payload with encoding.EncodeJSON and
	// republishes those exact bytes. Held as the narrow encoding.Unmarshaler
	// because bytes, not a transport, are all this needs.
	unmarshaler encoding.Unmarshaler

	appliedCounter  metrics.Int64Counter
	failedCounter   metrics.Int64Counter
	vanishedCounter metrics.Int64Counter
	lagHist         metrics.Float64Histogram
	applyHist       metrics.Float64Histogram

	indexAttr    metric.MeasurementOption
	upsertAttrs  metric.MeasurementOption
	deleteAttrs  metric.MeasurementOption
	invalidAttrs metric.MeasurementOption

	name string
}

// NewSyncer builds a Syncer that reads documents from source and writes them to
// target.
//
// name identifies the index in every span and every metric attribute. It must
// be unique within a process and stable across deploys — two syncers under one
// name give one lag reading covering both, and renaming one starts its history
// over.
func NewSyncer[T any](name string, source Fetcher[T], target Target[T], opts ...SyncerOption) (*Syncer[T], error) {
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

	indexAttr := attribute.String(keys.IndexNameKey, name)

	s := &Syncer[T]{
		name:         name,
		source:       source,
		target:       target,
		stamper:      o.stamper,
		clock:        o.clock,
		indexAttr:    metric.WithAttributes(indexAttr),
		upsertAttrs:  metric.WithAttributes(indexAttr, attribute.String(opKey, string(OpUpsert))),
		deleteAttrs:  metric.WithAttributes(indexAttr, attribute.String(opKey, string(OpDelete))),
		invalidAttrs: metric.WithAttributes(indexAttr, attribute.String(opKey, "invalid")),
	}

	// A Syncer serves exactly one index, so the index is stated here rather
	// than at each call site. Seeding the observer rather than only the logger
	// is what puts it on the spans as well as the log lines.
	s.o11y = observability.NewObserverWithValues(serviceName, o.logger, o.tracerProvider,
		map[string]any{keys.IndexNameKey: name})

	s.unmarshaler = encoding.NewClientEncoder(encoding.ContentTypeJSON,
		encoding.WithLogger(s.o11y.Logger()), encoding.WithTracerProvider(o.tracerProvider))

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	var err error
	if s.appliedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_events_applied", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating events applied counter")
	}
	if s.failedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_events_failed", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating events failed counter")
	}
	if s.vanishedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_documents_vanished", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating documents vanished counter")
	}
	if s.lagHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_lag_ms", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating lag histogram")
	}
	if s.applyHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_apply_latency_ms", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating apply latency histogram")
	}

	return s, nil
}

// Name returns the index name this Syncer was built with.
func (s *Syncer[T]) Name() string {
	return s.name
}

// Handle decodes one relayed event and applies it. It satisfies jobs.Handler,
// which is how it is meant to be used:
//
//	pool, err := jobs.NewPool(ctx, &jobs.PoolConfig{
//	    Topic:       "orders-index",
//	    Concurrency: 8,
//	}, consumerProvider, syncer.Handle, jobs.WithPoolDeadLetter(deadLetter))
//
// A payload that will not decode, or an event that is not applicable, is
// returned wrapped with retry.Unretryable so the Pool dead-letters it at once
// instead of failing the same way three more times while healthy events wait
// behind it.
func (s *Syncer[T]) Handle(ctx context.Context, payload []byte) error {
	var event Event
	if err := s.unmarshaler.Unmarshal(ctx, payload, &event); err != nil {
		s.failedCounter.Add(ctx, 1, s.invalidAttrs)

		return retry.Unretryable(platformerrors.Wrap(err, "decoding search sync event"))
	}

	return s.Apply(ctx, event)
}

// Apply brings the index into agreement with the source for one document.
//
// The event says which document changed; the source says what it now is. An
// upsert reads the row back and indexes whatever it finds — and finding nothing
// is a delete, not an error, because the source is what the index converges
// toward and the source says the document is gone. A delete needs no read.
//
// That indirection is what makes the sync order-insensitive. Two events for one
// document applied out of order both end up indexing the row's current state,
// and a redelivered event indexes it again, which is the same thing.
func (s *Syncer[T]) Apply(ctx context.Context, event Event) error {
	startTime := s.clock.Now()

	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(documentIDKey, event.DocumentID),
		observability.WithValue(opKey, string(event.Op)))
	defer op.End()

	if err := event.validate(); err != nil {
		s.failedCounter.Add(ctx, 1, s.invalidAttrs)

		return retry.Unretryable(op.Error(err, "validating search sync event"))
	}

	attrs := s.upsertAttrs
	if event.Op == OpDelete {
		attrs = s.deleteAttrs
	}

	// Recorded before the work rather than after it, so an event that fails
	// every attempt still reports how far behind it was. Lag is a property of
	// the event's arrival, not of the write that follows.
	s.recordLag(ctx, op, event, startTime)

	defer func() {
		s.applyHist.Record(ctx, float64(s.clock.Since(startTime).Milliseconds()), attrs)
	}()

	if err := s.apply(ctx, op, event); err != nil {
		s.failedCounter.Add(ctx, 1, attrs)

		return err
	}

	s.appliedCounter.Add(ctx, 1, attrs)

	return nil
}

// apply is the write itself, split out so Apply owns the accounting and this
// owns the decision.
func (s *Syncer[T]) apply(ctx context.Context, op observability.Operation, event Event) error {
	if event.Op == OpDelete {
		if err := s.target.Delete(ctx, event.DocumentID); err != nil {
			return op.Error(err, "deleting document %q from search index", event.DocumentID)
		}

		return nil
	}

	docs, err := s.source.Fetch(ctx, event.DocumentID)
	if err != nil {
		return op.Error(err, "fetching document %q for search index", event.DocumentID)
	}

	if len(docs) == 0 {
		// The row went away between the event being written and this moment.
		// Removing the document is the convergent answer, and it is also the
		// only one that terminates: leaving it means the index holds a document
		// no later event will ever mention again.
		op.Set(vanishedKey, true)
		s.vanishedCounter.Add(ctx, 1, s.indexAttr)

		if err = s.target.Delete(ctx, event.DocumentID); err != nil {
			return op.Error(err, "deleting vanished document %q from search index", event.DocumentID)
		}

		return nil
	}

	if err = s.target.Upsert(ctx, docs...); err != nil {
		return op.Error(err, "indexing document %q", event.DocumentID)
	}

	s.stamp(docs)

	return nil
}

// stamp records the documents the index just took, which is what maintains
// last_indexed_at on the rows behind them.
//
// It stamps what Fetch returned rather than the event's document ID. The two
// are the same in every ordinary case, and where they are not — a Fetcher that
// expands one changed row into the several documents derived from it — the
// documents are what the index accepted and therefore what the column is a
// statement about.
//
// It is reached only on the upsert path that wrote something, so every case
// that indexed nothing stamps nothing without needing to say so: a delete
// returns before it, a vanished row returns before it, and a failed Upsert
// returns the error.
func (s *Syncer[T]) stamp(docs []Document[T]) {
	if s.stamper == nil || len(docs) == 0 {
		return
	}

	ids := make([]string, 0, len(docs))
	for i := range docs {
		ids = append(ids, docs[i].ID)
	}

	s.stamper.Add(ids...)
}

// recordLag measures the event against the applying process's clock.
//
// A zero OccurredAt records nothing rather than an epoch-sized lag: an event
// built without one is not a claim that it is fifty-five years late. A negative
// difference — this process's clock behind the writer's — is floored at zero,
// because the histogram would otherwise carry a value that is a statement about
// clock skew wearing the units of latency.
func (s *Syncer[T]) recordLag(ctx context.Context, op observability.Operation, event Event, now time.Time) {
	if event.OccurredAt.IsZero() {
		return
	}

	lag := max(now.Sub(event.OccurredAt), 0)

	ms := float64(lag.Milliseconds())

	op.Set(lagKey, ms)
	s.lagHist.Record(ctx, ms, s.indexAttr)
}
