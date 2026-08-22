package metering

import (
	"context"
	"maps"
	"sync"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// PlanLimits is what one meter is worth on each product a subject can hold.
//
// It is the shape a consumer states their plan tiers in, and it is deliberately
// the smallest one that works: a number per product, a number for everybody
// else, and what happens at the number. Which products exist, what they cost,
// and when a subscription started are the billing provider's catalog and are not
// modeled here — see the package documentation on why there is only ever one
// catalog.
type PlanLimits struct {
	// ByProduct is the limit for a subject whose entitling product is that key.
	//
	// The key is whatever identifier the EntitlementReader returns — a Stripe
	// product or price ID, a plan slug, the primary key of a plans table. This
	// package never parses it and never compares it to anything but itself, so
	// the only requirement is that the two sides agree.
	//
	// A product missing from the map falls to Unsubscribed, which is the safe
	// direction and is counted and logged; see QuotaFor.
	ByProduct map[string]int64

	// Behavior is what happens at the limit for this meter, on every product.
	// Required.
	//
	// It is per meter rather than per product because it describes the meter's
	// nature — whether going over is a refusal, a warning, or the point at which
	// the price changes — and a meter that blocked on one tier and billed overage
	// on another would be two different meters.
	//
	// An empty behavior is refused at construction rather than defaulted, because
	// both defaults are bad ones: block would take a working endpoint down the
	// day somebody adds a meter to the table, and allow_overage would ship a
	// limit that never applies and says nothing about it.
	Behavior QuotaBehavior

	// Unsubscribed is the limit for a subject the EntitlementReader says no
	// product entitles: no subscription at all, or one that is past due,
	// cancelled, or never completed.
	//
	// Zero is a real value here and means no usage is allowed — a paid feature,
	// switched off until somebody pays for it. A free allowance is a small
	// number, and a meter that is free for everybody until they subscribe is
	// Unlimited. The three are different configurations and this package will not
	// conflate them.
	Unsubscribed int64
}

// validate reports whether the limits for a meter can be served.
func (l PlanLimits) validate(meter string) error {
	if !l.Behavior.Valid() {
		return platformerrors.Wrapf(ErrInvalidPlanLimits, "meter %q behavior %q", meter, l.Behavior)
	}

	if l.Unsubscribed < 0 {
		return platformerrors.Wrapf(ErrInvalidPlanLimits, "meter %q unsubscribed limit %d", meter, l.Unsubscribed)
	}

	// Iterated in sorted order so a table with two bad entries names the same one
	// on every run.
	for _, product := range sortedKeys(l.ByProduct) {
		if l.ByProduct[product] < 0 {
			return platformerrors.Wrapf(ErrInvalidPlanLimits,
				"meter %q product %q limit %d", meter, product, l.ByProduct[product])
		}
	}

	return nil
}

// clone copies the limits deeply enough that the source no longer shares state
// with the caller's table.
func (l PlanLimits) clone() PlanLimits {
	l.ByProduct = maps.Clone(l.ByProduct)

	return l
}

// EntitlementReader answers which product, if any, entitles a subject right now.
//
// This is the one thing a PlanLimitSource cannot supply, and the reason is worth
// stating: the join between an account and a live subscription is application
// data. It is written by the handler that processes the billing provider's
// subscription webhook, it is corrected by hand when somebody is grandfathered,
// and which statuses count as live is a product decision — trialing entitles,
// because a trial that quietly got the unsubscribed limits would be a trial of a
// different product, and past due generally does not, which is the lever that
// makes a lapsed payment degrade service rather than end it.
//
// It is also deliberately not a call to the billing provider. A provider round
// trip per quota check is a latency budget spent on a fact that changes a few
// times a year, and an outage there would take the product down rather than the
// billing. The provider says when a subscription changes; what it changed to
// belongs in the application's own database by the time anybody asks.
//
// entitled false means no product entitles the subject, which is an answer and
// not a failure — PlanLimits.Unsubscribed is what it resolves to. An error means
// the lookup could not be performed, which QuotaFor reports rather than guessing
// at; the enforcer's fail-open setting then decides what happens, in the one
// place that decision is configured.
//
// Implementations must be safe for concurrent use.
type EntitlementReader interface {
	EntitlingProduct(ctx context.Context, subject string) (productID string, entitled bool, err error)
}

// EntitlementReaderFunc adapts a function to EntitlementReader.
type EntitlementReaderFunc func(ctx context.Context, subject string) (productID string, entitled bool, err error)

// EntitlingProduct implements EntitlementReader.
func (f EntitlementReaderFunc) EntitlingProduct(ctx context.Context, subject string) (productID string, entitled bool, err error) {
	return f(ctx, subject)
}

var _ QuotaSource = (*PlanLimitSource)(nil)

// PlanLimitSource resolves a subject's quota from a table of per-product limits
// and a lookup of what they are subscribed to.
//
// It is the rung between the two answers this package otherwise offers — the
// Registry's one set of limits for every subject, and a QuotaSource written from
// scratch — and it exists because the ladder in the middle is the same in every
// subscription business and is not trivial to get right:
//
//  1. a meter absent from the limits table is unlimited, answered without
//     reading anything;
//  2. a subject an EntitlementReader entitles gets the limit for their product;
//  3. a subject it does not gets PlanLimits.Unsubscribed.
//
// The consumer supplies the numbers and the subscription lookup. This supplies
// the order they resolve in and the distinction between unlimited, unmetered,
// and zero — the part that is easy to get subtly wrong and hard to notice,
// because a wrong answer is either a customer blocked who should not be or a
// limit that silently never applies.
//
// It is not a plan catalog. If what you want is one — features that are not
// meters, plan-level flags, a single Check that answers "may this account use
// this at all" — that is the entitlements package, which holds a catalog and
// serves metering a QuotaSource off it. This is for the application that already
// knows its plans and wants only the limits resolved.
//
// A PlanLimitSource is safe for concurrent use and takes a copy of the limits
// table at construction, so a caller that goes on holding the map it passed
// cannot change what is being enforced halfway through a request.
//
// mu guards limits and the ByProduct map inside each entry. Nothing writes to
// either after construction, so the lock is uncontended in practice; it is here
// because a library does not get to decide how concurrently it is called, and an
// invariant that lives only in a comment is one a later writer breaks without
// noticing. See meterLimits and productLimit for why the two reads are taken
// separately rather than under one hold.
type PlanLimitSource struct {
	entitlements EntitlementReader
	o11y         observability.Observer

	unconfiguredCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this source actually uses; this one may
	// be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	registry        *Registry
	limits          map[string]PlanLimits

	mu sync.RWMutex
}

// NewPlanLimitSource builds a QuotaSource over a limits table and a subscription
// lookup.
//
// The registry is required and is read for two things: to check at construction
// that every meter the table names exists, and to read each meter's period.
// Deriving the period rather than taking it means a quota this source serves
// cannot mismatch the meter it is about — which is an error the enforcer would
// otherwise raise on the request path, for a mistake made at wiring time.
//
// A meter in the table that is not registered is refused here for the same
// reason. It is a typo in one of two names, and the shape of the resulting bug
// is the worst one available: the limit is never consulted, because nothing ever
// records against the meter it names, so the tier appears to work and is
// unlimited.
//
// The table may be empty. That is the honest starting position for a product
// that has not yet seen enough usage to set a limit from — everything is
// unlimited, the counting still happens, and the numbers go in once the totals
// say what real usage looks like.
func NewPlanLimitSource(
	registry *Registry,
	limits map[string]PlanLimits,
	entitlements EntitlementReader,
	opts ...PlanLimitOption,
) (*PlanLimitSource, error) {
	if registry == nil {
		return nil, ErrNilRegistry
	}

	if entitlements == nil {
		return nil, ErrNilEntitlementReader
	}

	s := &PlanLimitSource{
		registry:     registry,
		entitlements: entitlements,
		limits:       make(map[string]PlanLimits, len(limits)),
	}

	// Sorted, so a table with two problems in it reports the same one every run.
	for _, meter := range sortedKeys(limits) {
		if _, known := registry.Meter(meter); !known {
			return nil, platformerrors.Wrapf(ErrUnknownMeter, "plan limits for meter %q", meter)
		}

		entry := limits[meter]
		if err := entry.validate(meter); err != nil {
			return nil, err
		}

		s.limits[meter] = entry.clone()
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serviceName, s.logger, s.tracerProvider)

	if err := s.initInstruments(); err != nil {
		return nil, err
	}

	return s, nil
}

// initInstruments builds the source's meters.
func (s *PlanLimitSource) initInstruments() error {
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	counter, err := mp.NewInt64Counter(serviceName + "_unconfigured_products")
	if err != nil {
		return platformerrors.Wrap(err, "creating unconfigured product counter")
	}

	s.unconfiguredCounter = counter

	return nil
}

// meterLimits reports what the table says about a meter, and whether it names it
// at all.
//
// It hands back the two scalars rather than the PlanLimits holding them, because
// returning the struct would let its ByProduct map escape the read lock. The
// product lookup is productLimit, taken separately after the entitlement read —
// holding a lock across that read would put a database round trip inside the
// critical section, which is how an uncontended lock becomes a contended one.
func (s *PlanLimitSource) meterLimits(meter string) (QuotaBehavior, int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limits, gated := s.limits[meter]

	return limits.Behavior, limits.Unsubscribed, gated
}

// productLimit reports the limit configured for a product under a meter.
//
// A meter the table does not name yields no limit, which is the same answer as a
// product it does not price. Only QuotaFor's ladder distinguishes them, and it
// has already established the meter is gated before it asks.
func (s *PlanLimitSource) productLimit(meter, product string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit, configured := s.limits[meter].ByProduct[product]

	return limit, configured
}

// QuotaFor implements QuotaSource by walking the resolution ladder.
//
// A meter the table does not name is unlimited for every subject, and is
// answered before the entitlement lookup rather than after it. That short
// circuit is load-bearing: QuotaFor sits on Enforcer.Check's path, whose entire
// reason to exist is being cheaper than a durable round trip, and a subscription
// read per check to reach an answer identical for every subject would make the
// cheap path the expensive one.
//
// A subject entitled to a product nobody wrote a limit for gets the unsubscribed
// limit, counted on the metering_unconfigured_products counter and said out loud
// through the logger. The alternatives are both worse: unlimited would let a new
// tier ship with no limits and nobody notice, and an error would take the
// endpoint down for a customer whose only mistake was buying the plan somebody
// forgot to configure.
func (s *PlanLimitSource) QuotaFor(ctx context.Context, subject, meter string) (Quota, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		subjectKey: subject,
		meterKey:   meter,
	}))
	defer op.End()

	m, known := s.registry.Meter(meter)
	if !known {
		return Quota{}, op.Error(platformerrors.Wrapf(ErrUnknownMeter, "meter %q", meter), "resolving plan limits")
	}

	behavior, unsubscribed, gated := s.meterLimits(meter)
	if !gated {
		return unlimitedQuota(meter, m.Period), nil
	}

	limit := unsubscribed

	productID, entitled, err := s.entitlements.EntitlingProduct(ctx, subject)
	if err != nil {
		return Quota{}, op.Error(err, "reading the entitling product for subject %q", subject)
	}

	op.SetValues(map[string]any{
		productKey:  productID,
		entitledKey: entitled,
	})

	if entitled {
		if configuredLimit, configured := s.productLimit(meter, productID); configured {
			limit = configuredLimit
		} else {
			s.unconfiguredCounter.Add(ctx, 1, meterAttr(meter))
			op.Logger().Info("no plan limit configured for product; applying the unsubscribed limit")
		}
	}

	op.SetValues(map[string]any{
		limitKey:    limit,
		behaviorKey: string(behavior),
	})

	return Quota{
		Meter:    meter,
		Behavior: behavior,
		Period:   m.Period,
		Limit:    limit,
	}, nil
}

// unlimitedQuota is the quota for a meter no plan limits: a limit nobody reaches
// and a behavior that would let them past it anyway.
func unlimitedQuota(meter string, period Period) Quota {
	return Quota{
		Meter:    meter,
		Behavior: BehaviorAllowOverage,
		Period:   period,
		Limit:    Unlimited,
	}
}
