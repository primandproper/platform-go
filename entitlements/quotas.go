package entitlements

import (
	"context"
	"math"

	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/metering"
)

// UnlimitedLimit is the metering limit a QuotaSource reports for a grant with
// Grant.Unlimited set, paired with metering.BehaviorAllowOverage.
//
// metering has no unlimited, deliberately: its Quota.Limit of zero means none
// allowed, and a package that invoices cannot afford a value that means "do not
// enforce". So an unlimited grant is expressed in the vocabulary metering does
// have — a limit nobody reaches and a behavior that would let them past it
// anyway. Check never reads it, because an unlimited grant short-circuits before
// the enforcer is consulted; it exists for metering's own Consume path, which
// has to be told something.
const UnlimitedLimit int64 = math.MaxInt64

var _ metering.QuotaSource = (*QuotaSource)(nil)

// QuotaSource serves a Catalog's plan limits to metering.
//
// Wire it into the enforcer that the Checker will then consult:
//
//	quotas := entitlements.NewQuotaSource(catalog, plans, registry)
//	enforcer, err := metering.NewQuotaEnforcer(ctx, cfg, store, registry,
//	    metering.WithEnforcerQuotaSource(quotas))
//	checker, err := entitlements.NewPlanChecker(ctx, cfg, catalog, plans,
//	    entitlements.WithEnforcer(enforcer))
//
// This is the point of the whole arrangement, and it is worth being explicit
// about what it buys. metering asks a QuotaSource for a subject's limit and
// enforces whatever it is told; entitlements holds the plan catalog that knows
// what the limit is. Wired this way there is exactly one number — the catalog's
// — and the limit an account is shown is by construction the limit that is
// enforced against it.
//
// Wired any other way there are two, and they will disagree. A Checker notices
// when they do (see the entitlements_limit_mismatches counter) and reports what
// metering enforced rather than what the catalog claims, because the enforced
// number is the one the customer actually experiences.
//
// It is a separate type from Checker rather than a method on it because the two
// would otherwise have to be built in a cycle: the enforcer needs the quota
// source, and the Checker needs the enforcer. Splitting the catalog lookup out
// breaks it, and costs one line of wiring.
type QuotaSource struct {
	catalog  *Catalog
	plans    PlanSource
	registry *metering.Registry
}

// NewQuotaSource adapts a Catalog and a PlanSource to metering.QuotaSource.
//
// The registry is required and is used for two things: to check at construction
// that every quota feature names a meter that exists, and to read each meter's
// period. Deriving the period rather than taking it means a quota this source
// serves cannot mismatch the meter it is about — the error metering would
// otherwise raise on the request path, for a mistake made at wiring time.
func NewQuotaSource(catalog *Catalog, plans PlanSource, registry *metering.Registry) (*QuotaSource, error) {
	if catalog == nil {
		return nil, ErrNilCatalog
	}

	if plans == nil {
		return nil, ErrNilPlanSource
	}

	if registry == nil {
		return nil, ErrNilRegistry
	}

	if err := catalog.ValidateMeters(registry); err != nil {
		return nil, err
	}

	return &QuotaSource{catalog: catalog, plans: plans, registry: registry}, nil
}

// QuotaFor implements metering.QuotaSource.
//
// It resolves the subject's plan, finds the feature that counts against the
// meter, and reports what that plan grants. A meter no feature names, a plan
// that excludes the feature, and an account with no plan all report
// metering.ErrNoQuota — which metering reports rather than treating as
// unlimited, and which is the correct answer to "what may this subject consume
// of something their plan does not include".
//
// It does not cache. The Checker's cache sits in front of the request path this
// shares a PlanSource with, and metering consults this one only from Consume,
// which is already paying for a durable write — a cached plan there would save
// nothing measurable and would let an exact path be decided by a stale number.
func (q *QuotaSource) QuotaFor(ctx context.Context, subject, meter string) (metering.Quota, error) {
	m, ok := q.registry.Meter(meter)
	if !ok {
		return metering.Quota{}, platformerrors.Wrapf(metering.ErrUnknownMeter, "meter %q", meter)
	}

	f, ok := q.featureForMeter(meter)
	if !ok {
		return metering.Quota{}, platformerrors.Wrapf(metering.ErrNoQuota, "no entitlement feature meters %q", meter)
	}

	plan, err := q.plans.PlanFor(ctx, subject)
	if err != nil {
		return metering.Quota{}, platformerrors.Wrapf(err, "resolving plan for subject %q", subject)
	}

	grant, included := q.catalog.GrantFor(plan, f.Key)
	if !included {
		return metering.Quota{}, platformerrors.Wrapf(
			metering.ErrNoQuota, "plan %q does not include feature %q", plan, f.Key,
		)
	}

	quota := metering.Quota{
		Meter:    meter,
		Behavior: grant.Behavior,
		Period:   m.Period,
		Limit:    grant.Limit,
	}

	if grant.Unlimited {
		quota.Behavior = metering.BehaviorAllowOverage
		quota.Limit = UnlimitedLimit
	}

	if quota.Behavior == "" {
		quota.Behavior = metering.BehaviorBlock
	}

	return quota, nil
}

// featureForMeter finds the quota feature counting against a meter.
//
// The catalog is small and built once, so the scan is over a handful of entries
// and happens on metering's durable path rather than its cached one. An index
// would be a second map to keep consistent with the first for a lookup that does
// not appear in any profile.
func (q *QuotaSource) featureForMeter(meter string) (Feature, bool) {
	for _, key := range q.catalog.FeatureKeys() {
		f, ok := q.catalog.Feature(key)
		if ok && f.Kind == KindQuota && f.Meter == meter {
			return f, true
		}
	}

	return Feature{}, false
}
