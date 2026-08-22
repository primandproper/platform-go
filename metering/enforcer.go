package metering

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var _ Enforcer = (*QuotaEnforcer)(nil)

// CachedTotal is what the enforcer keeps in cache for a subject's period.
//
// It is the derived read cache and never the source of truth, which is the whole
// design of the read path. The alternative — buffering increments in the cache
// and reconciling them into the database later — makes the cache authoritative
// for the window between reconciliations, so losing a Redis instance loses usage
// that was never billed. Here a lost cache costs latency and nothing else: the
// next Check reads the durable total and repopulates.
type CachedTotal struct {
	// PeriodEnd is when the window closes, carried so a cache hit can answer
	// Decision.ResetsAt without re-resolving the period.
	PeriodEnd time.Time

	// Quantity is the durable total as of when this entry was written.
	Quantity int64
}

// QuotaEnforcer answers quota questions: cheaply and slightly stale through
// Check, exactly and durably through Consume.
type QuotaEnforcer struct {
	store    Store
	registry *Registry
	quotas   QuotaSource
	resolver PeriodResolver
	totals   cache.Cache[CachedTotal]
	clock    clock.Clock
	o11y     observability.Observer

	checkCounter    metrics.Int64Counter
	consumeCounter  metrics.Int64Counter
	deniedCounter   metrics.Int64Counter
	overageCounter  metrics.Int64Counter
	staleCounter    metrics.Int64Counter
	cacheErrCounter metrics.Int64Counter
	failOpenCounter metrics.Int64Counter
	checkHist       metrics.Float64Histogram
	consumeHist     metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read e.o11y.Logger() for the logger this enforcer actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg EnforcerConfig
}

// NewQuotaEnforcer builds the read path over a Store and a Registry.
//
// ctx is used to validate the config and is not retained.
func NewQuotaEnforcer(
	ctx context.Context,
	cfg *EnforcerConfig,
	store Store,
	registry *Registry,
	opts ...EnforcerOption,
) (*QuotaEnforcer, error) {
	if cfg == nil {
		return nil, platformerrors.New("nil metering enforcer config provided")
	}

	if store == nil {
		return nil, ErrNilStore
	}

	if registry == nil {
		return nil, ErrNilRegistry
	}

	cfg.EnsureDefaults()

	e := &QuotaEnforcer{
		cfg:      *cfg,
		store:    store,
		registry: registry,
		quotas:   NewRegistryQuotaSource(registry),
		resolver: NewCalendarPeriodResolver(nil),
		clock:    clock.NewClock(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}

	if err := e.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating metering enforcer config")
	}

	e.o11y = observability.NewObserver(serviceName, e.logger, e.tracerProvider)

	if err := e.initInstruments(); err != nil {
		return nil, err
	}

	if e.totals == nil {
		e.o11y.Logger().Info("metering enforcer has no cache; every Check will read the durable total")
	}

	return e, nil
}

// initInstruments builds the enforcer's meters.
func (e *QuotaEnforcer) initInstruments() error {
	mp := metrics.EnsureMetricsProvider(e.metricsProvider)

	var err error
	if e.checkCounter, err = mp.NewInt64Counter(serviceName + "_checks"); err != nil {
		return platformerrors.Wrap(err, "creating quota check counter")
	}
	if e.consumeCounter, err = mp.NewInt64Counter(serviceName + "_consumes"); err != nil {
		return platformerrors.Wrap(err, "creating quota consume counter")
	}
	if e.deniedCounter, err = mp.NewInt64Counter(serviceName + "_denied"); err != nil {
		return platformerrors.Wrap(err, "creating quota denial counter")
	}
	if e.overageCounter, err = mp.NewInt64Counter(serviceName + "_overage"); err != nil {
		return platformerrors.Wrap(err, "creating quota overage counter")
	}
	if e.staleCounter, err = mp.NewInt64Counter(serviceName + "_stale_checks"); err != nil {
		return platformerrors.Wrap(err, "creating stale check counter")
	}
	if e.cacheErrCounter, err = mp.NewInt64Counter(serviceName + "_cache_errors"); err != nil {
		return platformerrors.Wrap(err, "creating cache error counter")
	}
	if e.failOpenCounter, err = mp.NewInt64Counter(serviceName + "_fail_opens"); err != nil {
		return platformerrors.Wrap(err, "creating fail-open counter")
	}
	if e.checkHist, err = mp.NewFloat64Histogram(serviceName + "_check_latency_ms"); err != nil {
		return platformerrors.Wrap(err, "creating check latency histogram")
	}
	if e.consumeHist, err = mp.NewFloat64Histogram(serviceName + "_consume_latency_ms"); err != nil {
		return platformerrors.Wrap(err, "creating consume latency histogram")
	}

	return nil
}

// Check implements Enforcer.
func (e *QuotaEnforcer) Check(ctx context.Context, subject, meter string, quantity int64) (*Decision, error) {
	ctx, op := e.o11y.Begin(ctx, observability.WithValues(map[string]any{
		subjectKey:  subject,
		meterKey:    meter,
		quantityKey: quantity,
	}))
	defer op.End()

	defer op.Time(ctx, e.clock, e.checkHist, meterAttr(meter))()

	e.checkCounter.Add(ctx, 1, meterAttr(meter))

	m, quota, bounds, err := e.resolve(ctx, subject, meter)
	if err != nil {
		return nil, op.Error(err, "resolving metering quota")
	}

	annotatePeriod(op, m.Aggregation, bounds)

	used, stale, err := e.usage(ctx, op, m, subject, bounds)
	if err != nil {
		if !e.cfg.FailOpen {
			return nil, op.Error(err, "reading metering usage")
		}

		// Fail open: the caller proceeds, and the decision says so rather than
		// pretending to be a real reading. Used is left at zero and Stale is set,
		// which is the honest description of an answer derived from nothing.
		e.failOpenCounter.Add(ctx, 1, meterAttr(meter))
		op.Acknowledge(err, "reading metering usage; failing open")

		decision := newDecision(meter, quota.Behavior, 0, quota.Limit, bounds.End)
		decision.Stale = true

		return decision, nil
	}

	newer := true
	decision := newDecision(meter, quota.Behavior, m.Aggregation.Fold(used, quantity, newer), quota.Limit, bounds.End)
	decision.Stale = stale

	if stale {
		e.staleCounter.Add(ctx, 1, meterAttr(meter))
	}

	e.observeDecision(ctx, decision)
	e.annotate(op, decision)

	return decision, nil
}

// Consume implements Enforcer.
func (e *QuotaEnforcer) Consume(ctx context.Context, subject, meter string, quantity int64) (*Decision, error) {
	// A generated key, because this signature has none to offer. It makes each
	// call distinct, which is right for a call that is not being retried and
	// wrong for one that is — see the Enforcer interface, and reach for
	// ConsumeUsage on any path that can retry.
	return e.ConsumeUsage(ctx, Usage{
		Subject:        subject,
		Meter:          meter,
		Quantity:       quantity,
		IdempotencyKey: identifiers.New(),
	})
}

// ConsumeUsage implements Enforcer.
//
//nolint:gocritic // hugeParam: Usage is taken by value to match Recorder.Record's variadic
func (e *QuotaEnforcer) ConsumeUsage(ctx context.Context, u Usage) (*Decision, error) {
	ctx, op := e.o11y.Begin(ctx, observability.WithValues(map[string]any{
		subjectKey:  u.Subject,
		meterKey:    u.Meter,
		quantityKey: u.Quantity,
	}))
	defer op.End()

	defer op.Time(ctx, e.clock, e.consumeHist, meterAttr(u.Meter))()

	e.consumeCounter.Add(ctx, 1, meterAttr(u.Meter))

	if err := u.validate(); err != nil {
		return nil, op.Error(err, "validating metering usage")
	}

	m, quota, bounds, err := e.resolveAt(ctx, u.Subject, u.Meter, u.OccurredAt)
	if err != nil {
		return nil, op.Error(err, "resolving metering quota")
	}

	annotatePeriod(op, m.Aggregation, bounds)

	if u.OccurredAt.IsZero() {
		u.OccurredAt = e.clock.Now().UTC()
	}

	// No fail-open path. Consume's whole promise is that the answer is exact, and
	// an exact answer has nowhere to fail open to: allowing usage the store could
	// not record is allowing usage nobody will ever be billed for.
	decision, err := e.store.Consume(ctx, Entry{
		Usage:       u,
		Bounds:      bounds,
		Aggregation: m.Aggregation,
	}, quota.Limit, quota.Behavior, e.clock.Now().UTC())
	if err != nil {
		return nil, op.Error(err, "consuming metering quota")
	}

	// Written through rather than invalidated. The durable total is in hand and
	// is exactly what the next Check would read, so writing it costs one cache
	// round trip and saves the next reader a database one — and, more usefully,
	// closes the window in which a Check right after a Consume reports the total
	// from before it.
	e.writeThrough(ctx, op, u.Subject, u.Meter, bounds, decision.Used)

	e.observeDecision(ctx, decision)
	e.annotate(op, decision)

	return decision, nil
}

// resolve looks up the meter, its quota, and the current period.
func (e *QuotaEnforcer) resolve(ctx context.Context, subject, meter string) (Meter, Quota, Bounds, error) {
	return e.resolveAt(ctx, subject, meter, time.Time{})
}

// resolveAt is resolve for a given instant, defaulting to the clock's now.
func (e *QuotaEnforcer) resolveAt(ctx context.Context, subject, meter string, at time.Time) (Meter, Quota, Bounds, error) {
	m, ok := e.registry.Meter(meter)
	if !ok {
		return Meter{}, Quota{}, Bounds{}, platformerrors.Wrapf(ErrUnknownMeter, "meter %q", meter)
	}

	quota, err := e.quotas.QuotaFor(ctx, subject, meter)
	if err != nil {
		return Meter{}, Quota{}, Bounds{}, err
	}

	if quota.Period != m.Period {
		// A QuotaSource is application code and the Registry cannot vet what it
		// returns at wiring time, so the check that RegisterQuota runs once has
		// to run again here. A quota over the wrong window would read a total
		// nothing writes to, which presents as a limit that never fills.
		return Meter{}, Quota{}, Bounds{}, platformerrors.Wrapf(
			ErrPeriodMismatch, "meter %q has period %q, quota has %q", m.Name, m.Period, quota.Period,
		)
	}

	if at.IsZero() {
		at = e.clock.Now().UTC()
	}

	bounds, err := e.resolver.Resolve(ctx, subject, m.Period, at)
	if err != nil {
		return Meter{}, Quota{}, Bounds{}, err
	}

	return m, quota, bounds, nil
}

// usage reads the period's total, preferring the cache and falling back to the
// durable store.
func (e *QuotaEnforcer) usage(
	ctx context.Context,
	op observability.Operation,
	m Meter,
	subject string,
	bounds Bounds,
) (used int64, stale bool, err error) {
	key := e.cacheKey(subject, m.Name, bounds)

	if e.totals != nil {
		cached, cacheErr := e.totals.Get(ctx, key)
		switch {
		case cacheErr == nil && cached != nil:
			op.Set(cacheHitKey, true)

			return cached.Quantity, true, nil
		case cacheErr != nil && !errors.Is(cacheErr, cache.ErrNotFound):
			// Counted and carried on. A cache that is down turns Check into a
			// durable read, which is slow and correct — the wrong response to a
			// degraded cache is to stop answering.
			e.cacheErrCounter.Add(ctx, 1, meterAttr(m.Name))
			op.Acknowledge(cacheErr, "reading metering total from cache")
		}
	}

	op.Set(cacheHitKey, false)

	total, err := e.store.Total(ctx, subject, m.Name, bounds)
	if err != nil {
		return 0, false, err
	}

	e.writeThrough(ctx, op, subject, m.Name, bounds, total.Quantity)

	// Not stale: this came from the durable store this instant. The staleness
	// budget starts now, for whoever reads the cache entry next.
	return total.Quantity, false, nil
}

// writeThrough stores a total in the cache under the meter's staleness budget.
//
// The budget is the cache TTL and nothing else. There is no background
// reconciliation, no invalidation fan-out, and no versioning, because an entry
// that expires is an entry that gets re-read from the durable total — which
// bounds staleness by construction rather than by everybody remembering to
// invalidate.
func (e *QuotaEnforcer) writeThrough(
	ctx context.Context,
	op observability.Operation,
	subject, meter string,
	bounds Bounds,
	quantity int64,
) {
	if e.totals == nil {
		return
	}

	m, ok := e.registry.Meter(meter)
	if !ok {
		return
	}

	staleness := m.Staleness
	if staleness <= 0 {
		staleness = e.cfg.Staleness
	}

	// Never past the end of the period, and nothing at all for one that has
	// already closed. An entry that outlived its window would answer the next
	// period's first Check with the last period's total — a quota that starts
	// full on the first of the month — and a closed period is one nothing will
	// read again, so caching it is pure waste.
	//
	// A closed period is reachable through ConsumeUsage carrying an event time in
	// a past window, which is the ordinary shape of a queue redelivering late.
	remaining := bounds.End.Sub(e.clock.Now().UTC())
	if remaining <= 0 {
		return
	}

	staleness = min(staleness, remaining)

	entry := &CachedTotal{Quantity: quantity, PeriodEnd: bounds.End.UTC()}

	if err := e.totals.Set(ctx, e.cacheKey(subject, meter, bounds), entry, cache.WithExpiry(staleness)); err != nil {
		e.cacheErrCounter.Add(ctx, 1, meterAttr(meter))
		op.Acknowledge(err, "caching metering total")
	}
}

// cacheKey renders the cache key for one subject, meter, and period.
//
// The period start is part of the key rather than something the entry is checked
// against, so a new period is a new key and cannot be answered by the old one's
// entry. The alternative — one key per subject and meter, with the period stored
// inside — makes the rollover depend on every reader remembering to compare.
func (e *QuotaEnforcer) cacheKey(subject, meter string, bounds Bounds) string {
	return e.cfg.CachePrefix + subject + ":" + meter + ":" +
		strconv.FormatInt(bounds.Start.UTC().Unix(), 10)
}

// observeDecision records the instruments a decision moves.
func (e *QuotaEnforcer) observeDecision(ctx context.Context, decision *Decision) {
	attrs := metric.WithAttributes(
		attribute.String(meterKey, decision.Meter),
		attribute.String(behaviorKey, string(decision.Behavior)),
	)

	if !decision.Allowed {
		e.deniedCounter.Add(ctx, 1, attrs)
	}

	if decision.Overage > 0 {
		// The overage counter is what an overage invoice line is reconciled
		// against. It is a counter of units rather than of events, because the
		// question it answers is "how much did we let through past the limit" and
		// the answer is measured in the meter's unit.
		e.overageCounter.Add(ctx, decision.Overage, attrs)
	}
}

// annotatePeriod attaches the window a call is about, and how the meter's
// records fold into it.
//
// Set after the resolve rather than at Begin because neither is known before it:
// which window an instant falls in depends on the meter's bucketing and, for
// PeriodBillingPeriod, on the subject.
//
// Both bounds, not just the start. A total's window is recoverable from its start
// alone only for the calendar periods, and the case where it is not — a billing
// period an application resolved per subject, or a boundary that moved — is
// exactly the case somebody is reading a trace to understand.
func annotatePeriod(op observability.Operation, aggregation Aggregation, bounds Bounds) {
	op.SetValues(map[string]any{
		periodStartKey: bounds.Start,
		periodEndKey:   bounds.End,
		aggregationKey: string(aggregation),
	})
}

// annotate attaches a decision to the operation's span and logger.
func (e *QuotaEnforcer) annotate(op observability.Operation, decision *Decision) {
	op.SetValues(map[string]any{
		allowedKey:  decision.Allowed,
		usedKey:     decision.Used,
		limitKey:    decision.Limit,
		overageKey:  decision.Overage,
		behaviorKey: string(decision.Behavior),
		staleKey:    decision.Stale,
	})
}
