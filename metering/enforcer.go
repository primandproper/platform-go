package metering

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/primandproper/primitives-go/v2/cache"
	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var _ Enforcer = (*QuotaEnforcer)(nil)

// CachedTotal is what the enforcer keeps in cache for a subject's period: the
// durable total, and the quota that total is decided against.
//
// It keeps the total's name because the total is what the entry is keyed by and
// what the staleness budget was written for; the quota rides both rather than
// bringing its own. See the Quota field.
//
// It is the derived read cache and never the source of truth, which is the whole
// design of the read path. The alternative — buffering increments in the cache
// and reconciling them into the database later — makes the cache authoritative
// for the window between reconciliations, so losing a Redis instance loses usage
// that was never billed. Here a lost cache costs latency and nothing else: the
// next Check reads the durable total and repopulates.
type CachedTotal struct {
	// Quota is the quota that was resolved for this subject and meter when the
	// entry was written, cached beside the total rather than in an entry of its
	// own.
	//
	// It is here because a QuotaSource is not necessarily cheap. The wiring this
	// module recommends is entitlements.QuotaSource, which resolves the subject's
	// plan and reads a subscription row to answer QuotaFor, and a Check that
	// resolved a quota per call put that read on every request path a quota
	// guards — which is the opposite of what Check is for. Beside the total is
	// where it can live: the entry's key already names the scope, the subject, the
	// meter and the period, which is everything a quota is resolved from.
	//
	// One entry rather than two, so there is one expiry and one eviction. A second
	// entry would be a second staleness budget for somebody to reason about and a
	// Consume that has to remember to drop both; here the budget that bounds the
	// total bounds the quota, and the eviction that drops the total drops it.
	//
	// It is a pointer because its absence has to be distinguishable from a zero
	// value. An entry written before this field existed decodes through the
	// cache's codec with no error and no quota, and a zero Quota is a limit of
	// zero under an empty behavior — every request refused for the rest of the
	// budget, from a cache nobody can see into. So a nil quota is not a hit: the
	// Check that finds one resolves the quota, reads the durable total and
	// rewrites the entry, which is the cost a cold key already pays.
	Quota *Quota

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

	// reader is the executor Check falls back to when the cache cannot answer.
	//
	// Consume takes its transaction per call, because the write it makes belongs
	// in the caller's. Check makes no write and its callers hold no transaction
	// — an entitlement check in front of a cheap operation is the shape it is
	// written for — so its executor is settled once, here, rather than added to
	// a signature every caller would have to thread an argument through.
	reader database.SQLQueryExecutor

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
// reader is the executor Check reads the durable total on when the cache misses
// or is absent, and it is required: an enforcer that could not read the total
// would answer every cache miss by failing open or refusing, neither of which is
// a quota decision. database.Client.Reader() is the usual argument and is what
// this package did internally before the executor became the caller's to choose;
// a deployment whose replica lag would let a subject spend the same quota twice
// passes Writer() instead, which is now a decision visible at the wiring site.
//
// Consume takes no executor here. It is handed the caller's transaction per
// call, because the usage it records belongs in the transaction that did the
// work — see Enforcer.
//
// ctx is used to validate the config and is not retained.
func NewQuotaEnforcer(
	ctx context.Context,
	cfg *EnforcerConfig,
	store Store,
	registry *Registry,
	reader database.SQLQueryExecutor,
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

	if reader == nil {
		return nil, ErrNilExecutor
	}

	cfg.EnsureDefaults()

	e := &QuotaEnforcer{
		cfg:      *cfg,
		store:    store,
		registry: registry,
		reader:   reader,
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
func (e *QuotaEnforcer) Check(
	ctx context.Context,
	scope tenancy.Scope,
	subject, meter string,
	quantity int64,
) (*Decision, error) {
	ctx, op := e.o11y.Begin(ctx, observability.WithValues(map[string]any{
		scopeKey:    scope.String(),
		subjectKey:  subject,
		meterKey:    meter,
		quantityKey: quantity,
	}))
	defer op.End()

	// Refused before the counter, the timer, the quota resolve, and — the one
	// that matters — before the fail-open branch below can reach for it. An unset
	// scope is the caller's bug rather than a store that could not be read, and
	// failing open on it would answer a question nobody asked with a decision
	// that allows. It is not a check this deployment made, either, so it does not
	// land in the rate or the latency somebody watches.
	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "checking metering quota")
	}

	defer op.Time(ctx, e.clock, e.checkHist, meterAttr(meter))()

	e.checkCounter.Add(ctx, 1, meterAttr(meter))

	m, bounds, err := e.window(ctx, subject, meter, time.Time{})
	if err != nil {
		return nil, op.Error(err, "resolving metering quota")
	}

	annotatePeriod(op, m.Aggregation, bounds)

	// The cache first, and before the quota source rather than after it. An entry
	// carries both halves of a decision — the total and the quota it was resolved
	// against — so a Check inside the staleness budget consults neither the source
	// nor the store, and issues no statement at all. See CachedTotal.Quota.
	if entry := e.cached(ctx, op, m, scope, subject, bounds); entry != nil {
		return e.decide(ctx, op, m, *entry.Quota, entry.Quantity, quantity, bounds, true), nil
	}

	quota, err := e.quotaFor(ctx, m, subject)
	if err != nil {
		return nil, op.Error(err, "resolving metering quota")
	}

	total, err := e.store.Total(ctx, e.reader, scope, subject, m.Name, bounds)
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

	e.writeThrough(ctx, op, m, scope, subject, bounds, quota, total.Quantity)

	// Not stale: both halves came from their own sources this instant. The
	// staleness budget starts now, for whoever reads the entry next.
	return e.decide(ctx, op, m, quota, total.Quantity, quantity, bounds, false), nil
}

// decide folds the caller's quantity into a period's total, answers with it, and
// records what it answered.
//
// Both of Check's paths end here, which is what keeps the cheap one honest: an
// answer served from cache moves the same instruments and carries the same
// Decision.Stale as one that was read, rather than being a second decision shape
// nobody watches.
func (e *QuotaEnforcer) decide(
	ctx context.Context,
	op observability.Operation,
	m Meter,
	quota Quota,
	used, quantity int64,
	bounds Bounds,
	stale bool,
) *Decision {
	newer := true
	decision := newDecision(m.Name, quota.Behavior, m.Aggregation.Fold(used, quantity, newer), quota.Limit, bounds.End)
	decision.Stale = stale

	if stale {
		e.staleCounter.Add(ctx, 1, meterAttr(m.Name))
	}

	e.observeDecision(ctx, decision)
	e.annotate(op, decision)

	return decision
}

// Consume implements Enforcer.
func (e *QuotaEnforcer) Consume(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subject, meter string,
	quantity int64,
) (*Decision, error) {
	// A generated key, because this signature has none to offer. It makes each
	// call distinct, which is right for a call that is not being retried and
	// wrong for one that is — see the Enforcer interface, and reach for
	// ConsumeUsage on any path that can retry.
	return e.ConsumeUsage(ctx, tx, scope, Usage{
		Subject:        subject,
		Meter:          meter,
		Quantity:       quantity,
		IdempotencyKey: identifiers.New(),
	})
}

// ConsumeUsage implements Enforcer.
//
//nolint:gocritic // hugeParam: Usage is taken by value to match Recorder.Record's variadic
func (e *QuotaEnforcer) ConsumeUsage(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	u Usage,
) (*Decision, error) {
	ctx, op := e.o11y.Begin(ctx, observability.WithValues(map[string]any{
		scopeKey:    scope.String(),
		subjectKey:  u.Subject,
		meterKey:    u.Meter,
		quantityKey: u.Quantity,
	}))
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "consuming metering quota")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "consuming metering quota")
	}

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
	decision, err := e.store.Consume(ctx, tx, scope, Entry{
		Usage:       u,
		Bounds:      bounds,
		Aggregation: m.Aggregation,
	}, quota.Limit, quota.Behavior, e.clock.Now().UTC())
	if err != nil {
		return nil, op.Error(err, "consuming metering quota")
	}

	// Evicted rather than written through, because the total this decision was
	// made against is uncommitted: it lands when the caller's transaction does,
	// and this enforcer never learns whether that happened. Writing it through
	// would publish a total for a consume that rolled back, and refuse requests
	// for the rest of the staleness budget against usage nobody incurred.
	//
	// An eviction is right whichever way the transaction goes. If it commits,
	// the next Check reads the durable total, which is the number this call
	// produced; if it rolls back, the next Check reads the durable total, which
	// is the number from before it. Either way the cost is one cache miss.
	e.evict(ctx, op, scope, u.Subject, u.Meter, bounds)

	e.observeDecision(ctx, decision)
	e.annotate(op, decision)

	return decision, nil
}

// window resolves the meter and the period an instant falls in — everything a
// cache key is rendered from, and nothing a quota source is consulted for.
//
// It is separate from the quota deliberately. The key names the scope, the
// subject, the meter and the period, so a Check can look for a cached entry, and
// for the quota inside it, before it has resolved a quota at all. Resolving one
// first is what put entitlements' plan lookup and subscription read on every
// Check that the cache could have answered.
//
// at of the zero time means now.
func (e *QuotaEnforcer) window(ctx context.Context, subject, meter string, at time.Time) (Meter, Bounds, error) {
	m, ok := e.registry.Meter(meter)
	if !ok {
		return Meter{}, Bounds{}, platformerrors.Wrapf(ErrUnknownMeter, "meter %q", meter)
	}

	if at.IsZero() {
		at = e.clock.Now().UTC()
	}

	bounds, err := e.resolver.Resolve(ctx, subject, m.Period, at)
	if err != nil {
		return Meter{}, Bounds{}, err
	}

	return m, bounds, nil
}

// quotaFor asks the source what a subject may consume of a meter, and vets the
// answer against the meter it is about.
func (e *QuotaEnforcer) quotaFor(ctx context.Context, m Meter, subject string) (Quota, error) {
	quota, err := e.quotas.QuotaFor(ctx, subject, m.Name)
	if err != nil {
		return Quota{}, err
	}

	if quota.Period != m.Period {
		// A QuotaSource is application code and the Registry cannot vet what it
		// returns at wiring time, so the check that RegisterQuota runs once has
		// to run again here. A quota over the wrong window would read a total
		// nothing writes to, which presents as a limit that never fills.
		return Quota{}, platformerrors.Wrapf(
			ErrPeriodMismatch, "meter %q has period %q, quota has %q", m.Name, m.Period, quota.Period,
		)
	}

	return quota, nil
}

// resolveAt looks up the meter, the period an instant falls in, and a freshly
// read quota.
//
// It is Consume's resolve and deliberately not Check's. Consume's whole promise
// is that the answer is exact, and an exact answer cannot be made against a quota
// this process cached a staleness budget ago — so this path reads the source
// every time, which it can afford next to the durable write it is already making.
func (e *QuotaEnforcer) resolveAt(ctx context.Context, subject, meter string, at time.Time) (Meter, Quota, Bounds, error) {
	m, bounds, err := e.window(ctx, subject, meter, at)
	if err != nil {
		return Meter{}, Quota{}, Bounds{}, err
	}

	quota, err := e.quotaFor(ctx, m, subject)
	if err != nil {
		return Meter{}, Quota{}, Bounds{}, err
	}

	return m, quota, bounds, nil
}

// cached reads the entry for a scope, subject, meter and period, or reports nil
// for one that cannot answer a Check on its own.
//
// Three things are nil, and they are one instruction to the caller: resolve the
// quota and read the durable total. A miss is the ordinary one. A cache that
// cannot be reached is counted on the way past, for the reason in the comment
// below. And an entry carrying no quota was written before the quota was cached
// beside the total, which is half of what a decision needs — see
// CachedTotal.Quota.
func (e *QuotaEnforcer) cached(
	ctx context.Context,
	op observability.Operation,
	m Meter,
	scope tenancy.Scope,
	subject string,
	bounds Bounds,
) *CachedTotal {
	if e.totals != nil {
		entry, err := e.totals.Get(ctx, e.cacheKey(scope, subject, m.Name, bounds))
		switch {
		case err == nil && entry != nil && entry.Quota != nil:
			op.Set(cacheHitKey, true)

			return entry
		case err != nil && !errors.Is(err, cache.ErrNotFound):
			// Counted and carried on. A cache that is down turns Check into a
			// durable read, which is slow and correct — the wrong response to a
			// degraded cache is to stop answering.
			e.cacheErrCounter.Add(ctx, 1, meterAttr(m.Name))
			op.Acknowledge(err, "reading metering total from cache")
		}
	}

	op.Set(cacheHitKey, false)

	return nil
}

// evict drops a subject's cached entry for the period, so the next Check reads
// the durable total and resolves a quota again.
//
// Both, because it is one entry: the eviction that keeps a rolled-back Consume
// from publishing a total nobody incurred is the same eviction that lets a plan
// change reach Check before its staleness budget is up. See CachedTotal.Quota.
//
// A cache error is counted and swallowed, as it is on every other path here: a
// cache that cannot be reached leaves a stale entry that expires on its own
// within the staleness budget, and turning that into a failed Consume would make
// a degraded cache into a billing outage.
func (e *QuotaEnforcer) evict(
	ctx context.Context,
	op observability.Operation,
	scope tenancy.Scope,
	subject, meter string,
	bounds Bounds,
) {
	if e.totals == nil {
		return
	}

	if err := e.totals.Delete(ctx, e.cacheKey(scope, subject, meter, bounds)); err != nil {
		e.cacheErrCounter.Add(ctx, 1, meterAttr(meter))
		op.Acknowledge(err, "evicting metering total from cache")
	}
}

// writeThrough stores a total, and the quota it was decided against, under the
// meter's staleness budget.
//
// The budget is the cache TTL and nothing else. There is no background
// reconciliation, no invalidation fan-out, and no versioning, because an entry
// that expires is an entry that gets re-read from the durable total — which
// bounds staleness by construction rather than by everybody remembering to
// invalidate.
//
// The quota rides that same budget and that same key, so it gets no expiry knob
// of its own and no invalidation of its own. What it costs is stated rather than
// left to be found: a plan change reaches Check one staleness budget after it
// lands, and reaches Consume immediately, because Consume resolves the quota
// itself and evicts this entry on its way out.
func (e *QuotaEnforcer) writeThrough(
	ctx context.Context,
	op observability.Operation,
	m Meter,
	scope tenancy.Scope,
	subject string,
	bounds Bounds,
	quota Quota,
	quantity int64,
) {
	if e.totals == nil {
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

	entry := &CachedTotal{Quota: &quota, Quantity: quantity, PeriodEnd: bounds.End.UTC()}

	if err := e.totals.Set(ctx, e.cacheKey(scope, subject, m.Name, bounds), entry, cache.WithExpiry(staleness)); err != nil {
		e.cacheErrCounter.Add(ctx, 1, meterAttr(m.Name))
		op.Acknowledge(err, "caching metering total")
	}
}

// cacheKey renders the cache key for one scope, subject, meter, and period.
//
// The period start is part of the key rather than something the entry is checked
// against, so a new period is a new key and cannot be answered by the old one's
// entry. The alternative — one key per subject and meter, with the period stored
// inside — makes the rollover depend on every reader remembering to compare.
//
// The scope leads it, for the reason it leads the totals table's primary key: two
// tenants may name the same subject, and a key that could not tell them apart
// would answer one tenant's quota question with the other's usage — and would go
// on doing it for the staleness budget, from a cache nobody can see into. It is
// the scope's owner rather than its rendering, because String spells the global
// scope "<global>" for a human reading a log and a key is not that.
func (e *QuotaEnforcer) cacheKey(scope tenancy.Scope, subject, meter string, bounds Bounds) string {
	return e.cfg.CachePrefix + scope.Owner() + ":" + subject + ":" + meter + ":" +
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
