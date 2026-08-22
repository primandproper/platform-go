package metering

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/analytics"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// AnalyticsEvent is the event name usage is reported under when an
// analytics.EventReporter is attached.
const AnalyticsEvent = "metering.usage_recorded"

var _ Recorder = (*DurableRecorder)(nil)

// DurableRecorder is the ingest path: validate, resolve the period, dedupe, and
// fold into a durable total.
//
// It is a concrete type rather than an interface. Recorder is the interface, and
// the seams worth swapping — the Store, the PeriodResolver, the clock — are
// already interfaces with their own mocks.
type DurableRecorder struct {
	store     Store
	registry  *Registry
	resolver  PeriodResolver
	clock     clock.Clock
	o11y      observability.Observer
	analytics analytics.EventReporter

	recordedCounter  metrics.Int64Counter
	duplicateCounter metrics.Int64Counter
	droppedCounter   metrics.Int64Counter
	quantityCounter  metrics.Int64Counter
	latencyHist      metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read r.o11y.Logger() for the logger this recorder actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg RecorderConfig
}

// NewDurableRecorder builds the ingest path over a Store and a Registry.
//
// ctx is used to validate the config and is not retained.
func NewDurableRecorder(
	ctx context.Context,
	cfg *RecorderConfig,
	store Store,
	registry *Registry,
	opts ...RecorderOption,
) (*DurableRecorder, error) {
	if cfg == nil {
		return nil, platformerrors.New("nil metering recorder config provided")
	}

	if store == nil {
		return nil, ErrNilStore
	}

	if registry == nil {
		return nil, ErrNilRegistry
	}

	cfg.EnsureDefaults()

	r := &DurableRecorder{
		cfg:      *cfg,
		store:    store,
		registry: registry,
		clock:    clock.NewClock(),
		resolver: NewCalendarPeriodResolver(nil),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	if err := r.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating metering recorder config")
	}

	r.o11y = observability.NewObserver(serviceName, r.logger, r.tracerProvider)

	if err := r.initInstruments(); err != nil {
		return nil, err
	}

	return r, nil
}

// initInstruments builds the recorder's meters.
func (r *DurableRecorder) initInstruments() error {
	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

	var err error
	if r.recordedCounter, err = mp.NewInt64Counter(serviceName + "_usage_recorded"); err != nil {
		return platformerrors.Wrap(err, "creating usage recorded counter")
	}
	if r.duplicateCounter, err = mp.NewInt64Counter(serviceName + "_usage_duplicates"); err != nil {
		return platformerrors.Wrap(err, "creating usage duplicate counter")
	}
	if r.droppedCounter, err = mp.NewInt64Counter(serviceName + "_usage_dropped"); err != nil {
		return platformerrors.Wrap(err, "creating usage dropped counter")
	}
	if r.quantityCounter, err = mp.NewInt64Counter(serviceName + "_usage_quantity"); err != nil {
		return platformerrors.Wrap(err, "creating usage quantity counter")
	}
	if r.latencyHist, err = mp.NewFloat64Histogram(serviceName + "_ingest_latency_ms"); err != nil {
		return platformerrors.Wrap(err, "creating ingest latency histogram")
	}

	return nil
}

// Record implements Recorder.
func (r *DurableRecorder) Record(ctx context.Context, u ...Usage) error {
	return r.record(ctx, nil, u)
}

// RecordTx is Record inside the caller's transaction, so usage commits with
// whatever produced it.
//
// It is not on the Recorder interface because most call sites have no
// transaction to offer and would be forced to invent one. The ones that do — a
// row inserted and the storage it consumes, a message sent and the credit it
// spends — reach for this, and get the guarantee that a crash between the work
// and its usage record cannot leave the two disagreeing.
func (r *DurableRecorder) RecordTx(ctx context.Context, q database.SQLQueryExecutor, u ...Usage) error {
	if q == nil {
		return ErrNilExecutor
	}

	return r.record(ctx, q, u)
}

// record is the shared body: prepare every record, then hand the survivors to
// the store in configured chunks.
func (r *DurableRecorder) record(ctx context.Context, q database.SQLQueryExecutor, usages []Usage) error {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(batchSizeKey, len(usages)))
	defer op.End()

	if len(usages) == 0 {
		return nil
	}

	defer op.Time(ctx, r.clock, r.latencyHist)()

	now := r.clock.Now().UTC()

	entries, err := r.prepare(ctx, usages, now)
	if err != nil {
		return op.Error(err, "preparing metering usage")
	}

	if len(entries) == 0 {
		return nil
	}

	// Chunked so one Record call cannot exceed a driver's bind-parameter ceiling
	// or hold one transaction open across an unbounded batch. Each chunk is its
	// own transaction on the Record path; on the RecordTx path they share the
	// caller's, which is the point of that path.
	for chunk := range chunks(entries, r.cfg.BatchSize) {
		var result RecordResult

		if q == nil {
			result, err = r.store.Record(ctx, chunk, now)
		} else {
			result, err = r.store.RecordTx(ctx, q, chunk, now)
		}

		if err != nil {
			return op.Error(err, "recording metering usage")
		}

		r.observe(ctx, chunk, result)
	}

	return nil
}

// prepare validates each record, resolves its period, and attaches the meter's
// aggregation.
//
// A record naming an unknown meter is dropped and counted rather than failing the
// batch, unless RejectUnknownMeters says otherwise — see that field for why the
// default leans the way it does. Every other validation failure fails the batch,
// because those are the caller's own bug and are the same on every retry.
func (r *DurableRecorder) prepare(ctx context.Context, usages []Usage, now time.Time) ([]Entry, error) {
	entries := make([]Entry, 0, len(usages))

	for i := range usages {
		u := usages[i]

		if err := u.validate(); err != nil {
			return nil, err
		}

		m, ok := r.registry.Meter(u.Meter)
		if !ok {
			if r.cfg.RejectUnknownMeters {
				return nil, platformerrors.Wrapf(ErrUnknownMeter, "meter %q", u.Meter)
			}

			r.droppedCounter.Add(ctx, 1, meterAttr(u.Meter))
			r.o11y.Logger().WithValues(map[string]any{
				meterKey:   u.Meter,
				subjectKey: u.Subject,
			}).Info("dropping metering usage for an unregistered meter")

			continue
		}

		if u.OccurredAt.IsZero() {
			u.OccurredAt = now
		}

		bounds, err := r.resolver.Resolve(ctx, u.Subject, m.Period, u.OccurredAt)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "resolving period for meter %q", u.Meter)
		}

		entries = append(entries, Entry{
			Usage:       u,
			Bounds:      bounds,
			Aggregation: m.Aggregation,
		})
	}

	return entries, nil
}

// observe records the instruments and emits the analytics events for one
// accepted chunk.
//
// The analytics fan-out runs over the whole chunk rather than only the accepted
// records, and that is a deliberate imprecision: the store reports how many were
// new but not which, and re-deriving it would cost the round trip the batching
// exists to avoid. An analytics warehouse that occasionally sees a redelivered
// event is a warehouse doing its job; the number that must not double-count is
// the durable total, and that one is exact.
func (r *DurableRecorder) observe(ctx context.Context, chunk []Entry, result RecordResult) {
	if result.Accepted > 0 {
		r.recordedCounter.Add(ctx, int64(result.Accepted))
	}

	if result.Duplicates > 0 {
		r.duplicateCounter.Add(ctx, int64(result.Duplicates))
	}

	for i := range chunk {
		r.quantityCounter.Add(ctx, chunk[i].Quantity, meterAttr(chunk[i].Meter))
	}

	if r.analytics == nil || result.Accepted == 0 {
		return
	}

	for i := range chunk {
		entry := &chunk[i]

		if err := r.analytics.EventOccurred(ctx, AnalyticsEvent, entry.Subject, entry.analyticsProperties()); err != nil {
			// Logged and swallowed. Analytics is a side channel: an ingest path
			// that failed because a warehouse was unreachable would be a metering
			// outage caused by a system nobody bills from.
			r.o11y.Logger().WithValue(meterKey, entry.Meter).
				Error("reporting metering usage to analytics", err)
		}
	}
}

// analyticsProperties renders one record for an analytics reporter.
//
// Dimensions are flattened under their own keys rather than nested, because most
// warehouses index top-level properties and nest nothing. They are prefixed so a
// dimension called "meter" cannot overwrite the meter.
func (e *Entry) analyticsProperties() map[string]any {
	properties := map[string]any{
		"meter":        e.Meter,
		"quantity":     e.Quantity,
		"occurred_at":  e.OccurredAt.UTC(),
		"period_start": e.Bounds.Start.UTC(),
		"period_end":   e.Bounds.End.UTC(),
	}

	for k, v := range e.Dimensions {
		properties["dimension_"+k] = v
	}

	return properties
}

// meterAttr labels an instrument with the meter it is about.
//
// The meter and nothing else. Labeling by subject would give every instrument a
// cardinality equal to the customer count, which is how a metrics bill comes to
// exceed the revenue the metering was measuring.
func meterAttr(meter string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(meterKey, meter))
}

// chunks yields successive slices of at most size elements.
func chunks[T any](items []T, size int) func(func([]T) bool) {
	return func(yield func([]T) bool) {
		for start := 0; start < len(items); start += size {
			if !yield(items[start:min(start+size, len(items))]) {
				return
			}
		}
	}
}
