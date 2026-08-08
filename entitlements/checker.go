package entitlements

import (
	"context"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v10/authorization"
	"github.com/primandproper/platform-go/v10/cache"
	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/featureflags"
	"github.com/primandproper/platform-go/v10/metering"
	"github.com/primandproper/platform-go/v10/observability"
	"github.com/primandproper/platform-go/v10/observability/logging"
	"github.com/primandproper/platform-go/v10/observability/metrics"
	"github.com/primandproper/platform-go/v10/observability/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var _ Checker = (*PlanChecker)(nil)

// Assignment is what the checker keeps in cache for an account: the plan it is
// on.
//
// The plan name and nothing else, deliberately. What that plan includes is an
// in-memory map lookup against the Catalog, so caching a resolved feature set
// would spend cache bytes and an encoding to save two map reads — and would make
// every catalog change require a cache flush, turning a deploy into a
// coordination problem.
type Assignment struct {
	// Plan is the plan name the PlanSource returned.
	Plan string
}

// PlanChecker answers entitlement questions from a Catalog, a PlanSource, and —
// where the feature is quota-bound or flagged — metering and feature flags.
type PlanChecker struct {
	catalog  *Catalog
	plans    PlanSource
	enforcer metering.Enforcer
	flags    featureflags.FeatureFlagManager
	cache    cache.Cache[Assignment]
	o11y     observability.Observer
	logger   logging.Logger

	checkCounter     metrics.Int64Counter
	deniedCounter    metrics.Int64Counter
	cacheErrCounter  metrics.Int64Counter
	planFaultCounter metrics.Int64Counter
	flagErrCounter   metrics.Int64Counter
	mismatchCounter  metrics.Int64Counter
	checkHist        metrics.Float64Histogram

	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg CheckerConfig
}

// NewPlanChecker builds a Checker over a Catalog and a PlanSource.
//
// ctx is used to validate the config and is not retained.
func NewPlanChecker(
	ctx context.Context,
	cfg *CheckerConfig,
	catalog *Catalog,
	plans PlanSource,
	opts ...CheckerOption,
) (*PlanChecker, error) {
	if cfg == nil {
		return nil, platformerrors.New("nil entitlements checker config provided")
	}

	if catalog == nil {
		return nil, ErrNilCatalog
	}

	if plans == nil {
		return nil, ErrNilPlanSource
	}

	cfg.EnsureDefaults()

	c := &PlanChecker{cfg: *cfg, catalog: catalog, plans: plans}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}

	if err := c.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating entitlements checker config")
	}

	// A catalog with quota features and no enforcer fails here rather than at the
	// first Check of one. The other way round, a service would pass every test
	// written for the boolean features it does answer and fail in production on
	// the one that costs money.
	if catalog.HasQuotaFeatures() && c.enforcer == nil {
		return nil, ErrEnforcerRequired
	}

	// A fallback naming a plan nobody registered would deny everything during
	// exactly the outage it was configured to survive, and would do it silently,
	// because the only way to find out is to have the outage.
	if c.cfg.FallbackPlan != "" {
		if _, ok := catalog.Plan(c.cfg.FallbackPlan); !ok {
			return nil, platformerrors.Wrapf(ErrUnknownPlan, "fallback plan %q", c.cfg.FallbackPlan)
		}
	}

	c.o11y = observability.NewObserver(serviceName, c.logger, c.tracerProvider)
	c.logger = c.o11y.Logger()

	if err := c.initInstruments(); err != nil {
		return nil, err
	}

	if c.cache == nil {
		c.logger.Info("entitlements checker has no cache; every check will resolve the account's plan")
	}

	return c, nil
}

// initInstruments builds the checker's meters.
func (c *PlanChecker) initInstruments() error {
	mp := metrics.EnsureMetricsProvider(c.metricsProvider)

	var err error
	if c.checkCounter, err = mp.NewInt64Counter(serviceName + "_checks"); err != nil {
		return platformerrors.Wrap(err, "creating entitlement check counter")
	}
	if c.deniedCounter, err = mp.NewInt64Counter(serviceName + "_denied"); err != nil {
		return platformerrors.Wrap(err, "creating entitlement denial counter")
	}
	if c.cacheErrCounter, err = mp.NewInt64Counter(serviceName + "_cache_errors"); err != nil {
		return platformerrors.Wrap(err, "creating entitlement cache error counter")
	}
	if c.planFaultCounter, err = mp.NewInt64Counter(serviceName + "_plan_faults"); err != nil {
		return platformerrors.Wrap(err, "creating entitlement plan fault counter")
	}
	if c.flagErrCounter, err = mp.NewInt64Counter(serviceName + "_flag_errors"); err != nil {
		return platformerrors.Wrap(err, "creating entitlement flag error counter")
	}
	if c.mismatchCounter, err = mp.NewInt64Counter(serviceName + "_limit_mismatches"); err != nil {
		return platformerrors.Wrap(err, "creating entitlement limit mismatch counter")
	}
	if c.checkHist, err = mp.NewFloat64Histogram(serviceName + "_check_latency_ms"); err != nil {
		return platformerrors.Wrap(err, "creating entitlement check latency histogram")
	}

	return nil
}

// Check implements Checker.
func (c *PlanChecker) Check(ctx context.Context, account, feature string) (*Decision, error) {
	return c.CheckQuantity(ctx, account, feature, 1)
}

// CheckQuantity implements Checker.
func (c *PlanChecker) CheckQuantity(ctx context.Context, account, feature string, quantity int64) (*Decision, error) {
	startTime := time.Now()

	ctx, op := c.o11y.Begin(ctx, observability.WithValues(map[string]any{
		accountKey:  account,
		featureKey:  feature,
		quantityKey: quantity,
	}))
	defer op.End()

	defer func() {
		c.checkHist.Record(ctx, float64(time.Since(startTime).Milliseconds()), featureAttr(feature))
	}()

	c.checkCounter.Add(ctx, 1, featureAttr(feature))

	if account == "" {
		return nil, op.Error(ErrEmptyAccount, "checking entitlement")
	}

	f, ok := c.catalog.Feature(feature)
	if !ok {
		// An error rather than a denial: an unregistered key is a typo in the
		// calling code, and answering "your plan does not include it" would have
		// somebody ship a permanently dark feature and open a billing ticket.
		return nil, op.Error(platformerrors.Wrapf(ErrUnknownFeature, "feature %q", feature), "checking entitlement")
	}

	op.Set(kindKey, string(f.Kind))

	// The kill flag is evaluated before the plan is resolved, because it is the
	// answer that does not depend on one — and because the reason to reach for a
	// kill switch is usually that something downstream is on fire, which is the
	// worst moment to make the answer require another lookup.
	if c.flagEnabled(ctx, op, account, f.KillFlag) {
		return c.finish(ctx, op, &Decision{
			Feature:    f.Key,
			Kind:       f.Kind,
			Reason:     ReasonFlagKilled,
			Permission: f.permission(),
			Limit:      Unbounded,
			Remaining:  Unbounded,
		}), nil
	}

	plan, stale, reason := c.resolvePlan(ctx, op, account)
	if reason != "" {
		return c.finish(ctx, op, &Decision{
			Feature:    f.Key,
			Plan:       plan,
			Kind:       f.Kind,
			Reason:     reason,
			Permission: f.permission(),
			Limit:      Unbounded,
			Remaining:  Unbounded,
			Stale:      stale,
		}), nil
	}

	op.Set(planKey, plan)

	grant, included := c.catalog.GrantFor(plan, f.Key)

	if f.Kind == KindBoolean {
		return c.finish(ctx, op, c.decideBoolean(ctx, op, account, &f, plan, included, stale)), nil
	}

	return c.decideQuota(ctx, op, account, &f, plan, grant, included, quantity, stale)
}

// decideBoolean answers a boolean feature from the plan and the grant flag.
func (c *PlanChecker) decideBoolean(
	ctx context.Context,
	op observability.Operation,
	account string,
	f *Feature,
	plan string,
	included, stale bool,
) *Decision {
	d := &Decision{
		Feature:    f.Key,
		Plan:       plan,
		Kind:       KindBoolean,
		Permission: f.permission(),
		Limit:      Unbounded,
		Remaining:  Unbounded,
		Stale:      stale,
	}

	switch {
	case included:
		d.Allowed, d.Reason = true, ReasonPlanIncludes
	case c.flagEnabled(ctx, op, account, f.GrantFlag):
		d.Allowed, d.Reason = true, ReasonFlagGranted
	default:
		d.Reason = ReasonPlanExcludes
	}

	return d
}

// decideQuota answers a quota feature from the plan's grant and metering.
func (c *PlanChecker) decideQuota(
	ctx context.Context,
	op observability.Operation,
	account string,
	f *Feature,
	plan string,
	grant Grant,
	included bool,
	quantity int64,
	stale bool,
) (*Decision, error) {
	d := &Decision{
		Feature:    f.Key,
		Plan:       plan,
		Kind:       KindQuota,
		Permission: f.permission(),
		Limit:      Unbounded,
		Remaining:  Unbounded,
		Stale:      stale,
	}

	if !included {
		d.Reason = ReasonPlanExcludes

		return c.finish(ctx, op, d), nil
	}

	if grant.Unlimited {
		// No usage read at all. There is no total that would change the answer,
		// so paying metering's round trip here would be spending latency to learn
		// nothing. Usage is still being recorded by whatever calls the Recorder;
		// this only declines to read it.
		d.Allowed, d.Reason, d.Unlimited = true, ReasonUnlimited, true

		return c.finish(ctx, op, d), nil
	}

	decision, err := c.enforcer.Check(ctx, account, f.Meter, quantity)
	if err != nil {
		return nil, op.Error(err, "checking metering quota for feature %q", f.Key)
	}

	c.noteLimitMismatch(ctx, op, f, plan, grant, decision)

	d.Limit = decision.Limit
	d.Used = decision.Used
	d.Remaining = max(0, decision.Limit-decision.Used)
	d.Overage = decision.Overage
	d.Allowed = decision.Allowed
	d.ResetsAt = decision.ResetsAt
	d.Stale = stale || decision.Stale

	switch {
	case !d.Allowed:
		d.Reason = ReasonQuotaExhausted
	case d.Overage > 0:
		d.Reason = ReasonQuotaOverage
	default:
		d.Reason = ReasonQuotaAvailable
	}

	return c.finish(ctx, op, d), nil
}

// noteLimitMismatch counts an enforcer whose limit is not the catalog's.
//
// The two agree by construction when the enforcer was wired with this package's
// QuotaSource, which is the documented arrangement. When they do not, the
// decision reports what metering enforced — the number the customer actually
// experiences — and this is the only place the disagreement is visible. It is a
// counter rather than an error because the answer is still correct; it is the
// catalog that has stopped being true.
func (c *PlanChecker) noteLimitMismatch(
	ctx context.Context,
	op observability.Operation,
	f *Feature,
	plan string,
	grant Grant,
	decision *metering.Decision,
) {
	if decision.Limit == grant.Limit {
		return
	}

	c.mismatchCounter.Add(ctx, 1, featureAttr(f.Key))
	op.Logger().WithValues(map[string]any{
		planKey:          plan,
		featureKey:       f.Key,
		meterKey:         f.Meter,
		catalogLimitKey:  grant.Limit,
		enforcedLimitKey: decision.Limit,
	}).Info("entitlement grant limit differs from the limit metering enforced; see entitlements.NewQuotaSource")
}

// Permissions implements Checker.
func (c *PlanChecker) Permissions(ctx context.Context, account string) (*authorization.PermissionSet, error) {
	ctx, op := c.o11y.Begin(ctx, observability.WithValue(accountKey, account))
	defer op.End()

	if account == "" {
		return nil, op.Error(ErrEmptyAccount, "resolving entitlement permissions")
	}

	plan, _, reason := c.resolvePlan(ctx, op, account)
	if reason != "" {
		// The empty set rather than an error. This is called while building a
		// session, and a session build that fails because a plan could not be
		// read logs the holder out of a product they are still entitled to most
		// of. An account with no plan simply carries no entitlement permissions,
		// which is what every downstream check already handles.
		op.Set(reasonKey, string(reason))

		return authorization.NewPermissionSet(), nil
	}

	op.Set(planKey, plan)

	var perms []authorization.Permission
	for _, key := range c.catalog.FeatureKeys() {
		f, ok := c.catalog.Feature(key)
		if !ok || f.Kind != KindBoolean {
			continue
		}

		if c.flagEnabled(ctx, op, account, f.KillFlag) {
			continue
		}

		_, included := c.catalog.GrantFor(plan, f.Key)
		if included || c.flagEnabled(ctx, op, account, f.GrantFlag) {
			perms = append(perms, f.permission())
		}
	}

	op.SpanOnly(permCountKey, len(perms))

	return authorization.NewPermissionSet(perms...), nil
}

// Invalidate drops the cached plan assignment for an account.
//
// This is what the handler for a subscription webhook calls once it has written
// the new plan, so the customer who just upgraded is not told for another TTL
// that they have not. Without a cache it is a no-op.
//
// It is process-local in the sense that matters: it removes the entry from a
// shared cache, so every replica sees the change, but a replica mid-request has
// already read what it read. Unlike a read fault, a failure here is returned
// rather than degraded around — the caller asked for a stale plan to stop being
// served, and silently not doing that is the bug they were trying to avoid.
func (c *PlanChecker) Invalidate(ctx context.Context, account string) error {
	if c.cache == nil || account == "" {
		return nil
	}

	ctx, op := c.o11y.Begin(ctx, observability.WithValue(accountKey, account))
	defer op.End()

	if err := c.cache.Delete(ctx, c.cacheKey(account)); err != nil {
		return op.Error(err, "invalidating cached plan assignment")
	}

	return nil
}

// resolvePlan reads the account's plan, preferring the cache.
//
// The third return is the Reason to deny with, and is empty when a plan was
// resolved. Three distinct outcomes hide behind it, and keeping them apart is
// the point: ReasonNoPlan is an account that has not paid, ReasonUnknownPlan is
// a catalog that has drifted from the plan store, and ReasonPlanUnavailable is
// the plan store being down with no fallback configured. They look identical to
// the customer and want three different people woken up.
func (c *PlanChecker) resolvePlan(
	ctx context.Context,
	op observability.Operation,
	account string,
) (plan string, stale bool, reason Reason) {
	key := c.cacheKey(account)

	if c.cache != nil {
		cached, err := c.cache.Get(ctx, key)
		switch {
		case err == nil && cached != nil:
			op.SpanOnly(cacheHitKey, true)

			return c.validPlan(cached.Plan, true)
		case err != nil && !errors.Is(err, cache.ErrNotFound):
			// Counted and carried on. A cache that is down turns a check into a
			// plan lookup, which is slower and correct — the wrong response to a
			// degraded cache is to stop answering.
			c.cacheErrCounter.Add(ctx, 1)
			op.Acknowledge(err, "reading cached plan assignment")
		}
	}

	op.SpanOnly(cacheHitKey, false)

	resolved, err := c.plans.PlanFor(ctx, account)
	switch {
	case err == nil:
	case errors.Is(err, ErrNoPlan):
		// An answer, not a failure, and so not counted as a fault: this account
		// genuinely has no plan.
		return "", false, ReasonNoPlan
	default:
		c.planFaultCounter.Add(ctx, 1)
		op.Acknowledge(err, "resolving plan for account")

		if c.cfg.FallbackPlan == "" {
			return "", false, ReasonPlanUnavailable
		}

		// Degraded to a plan somebody chose, and marked stale so a caller that
		// cares can tell. The fallback is deliberately not cached: writing it
		// would extend a momentary plan-store blip into a full TTL of every
		// account being on the fallback tier.
		op.SpanOnly(fallbackKey, true)

		return c.validPlan(c.cfg.FallbackPlan, true)
	}

	if c.cache != nil {
		if err = c.cache.Set(ctx, key, &Assignment{Plan: resolved}, cache.WithExpiry(c.cfg.CacheTTL)); err != nil {
			// The answer is already correct; failing to memoize it is not a
			// reason to fail the caller. It is a reason to count it: a write that
			// keeps failing turns every request into a plan lookup, which reads
			// as a cache that is merely cold rather than one that is broken.
			c.cacheErrCounter.Add(ctx, 1)
			op.Acknowledge(err, "caching plan assignment")
		}
	}

	return c.validPlan(resolved, false)
}

// validPlan checks a resolved plan name against the catalog.
func (c *PlanChecker) validPlan(name string, cached bool) (plan string, stale bool, reason Reason) {
	if name == "" {
		return "", cached, ReasonNoPlan
	}

	if _, ok := c.catalog.Plan(name); !ok {
		return name, cached, ReasonUnknownPlan
	}

	return name, cached, ""
}

// flagEnabled evaluates a feature flag for an account.
//
// An unnamed flag, an absent flag manager, a flag the provider does not know,
// and a provider that errored all answer false, and that uniformity is the
// design rather than a coincidence. The last two are reported as errors and are
// indistinguishable from one another — a provider cannot tell "this flag does
// not exist" from "I cannot reach the service that would know" — so this package
// only ever asks questions whose false answer is inert: a grant flag that is
// false defers to the plan, and a kill flag that is false defers to the plan.
// Neither direction can be flipped by a flag nobody has created or a provider
// nobody can reach.
//
// The error is counted rather than returned, and a sustained rise in
// entitlements_flag_errors is worth an alert. It usually means a flag named in
// this catalog was never created in the provider — a rollout that will never
// happen, and, because the providers here score an unresolvable flag against
// their circuit breaker, an evaluation that can eventually take the rest of the
// process's flags down with it. See issue #126.
func (c *PlanChecker) flagEnabled(ctx context.Context, op observability.Operation, account, flag string) bool {
	if flag == "" || c.flags == nil {
		return false
	}

	enabled, err := c.flags.CanUseFeature(ctx, flag, featureflags.EvaluationContext{TargetingKey: account})
	if err != nil {
		c.flagErrCounter.Add(ctx, 1, metric.WithAttributes(attribute.String(flagKey, flag)))
		op.Acknowledge(err, "evaluating entitlement feature flag %q", flag)

		return false
	}

	return enabled
}

// cacheKey renders the cache key for one account's plan assignment.
func (c *PlanChecker) cacheKey(account string) string {
	return c.cfg.CachePrefix + account
}

// finish records a decision's instruments and annotates the operation.
func (c *PlanChecker) finish(ctx context.Context, op observability.Operation, d *Decision) *Decision {
	if !d.Allowed {
		c.deniedCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String(featureKey, d.Feature),
			attribute.String(reasonKey, string(d.Reason)),
		))
	}

	op.SetValues(map[string]any{
		allowedKey:   d.Allowed,
		reasonKey:    string(d.Reason),
		limitKey:     d.Limit,
		usedKey:      d.Used,
		remainingKey: d.Remaining,
		staleKey:     d.Stale,
	})

	return d
}

// featureAttr is the metric attribute set naming a feature.
func featureAttr(feature string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(featureKey, feature))
}
