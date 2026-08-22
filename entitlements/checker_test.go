package entitlements

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authorization"
	"github.com/primandproper/platform-go/v13/cache"
	cachemock "github.com/primandproper/platform-go/v13/cache/mock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/featureflags"
	"github.com/primandproper/platform-go/v13/metering"
	meteringmock "github.com/primandproper/platform-go/v13/metering/mock"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewPlanChecker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, staticPlans(planPro))

		test.NotNil(t, c)
		test.EqOp(t, DefaultCacheTTL, c.cfg.CacheTTL)
		test.EqOp(t, DefaultCachePrefix, c.cfg.CachePrefix)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewPlanChecker(t.Context(), nil, newBooleanCatalog(t), staticPlans(planPro))

		test.Error(t, err)
	})

	T.Run("rejects a nil catalog", func(t *testing.T) {
		t.Parallel()

		_, err := NewPlanChecker(t.Context(), &CheckerConfig{}, nil, staticPlans(planPro))

		test.ErrorIs(t, err, ErrNilCatalog)
	})

	T.Run("rejects a nil plan source", func(t *testing.T) {
		t.Parallel()

		_, err := NewPlanChecker(t.Context(), &CheckerConfig{}, newBooleanCatalog(t), nil)

		test.ErrorIs(t, err, ErrNilPlanSource)
	})

	T.Run("rejects a quota catalog with no enforcer", func(t *testing.T) {
		t.Parallel()

		// The failure this catches is a service that passes every test written
		// for its boolean features and fails in production on the one that costs
		// money.
		_, err := NewPlanChecker(t.Context(), &CheckerConfig{}, newCatalog(t), staticPlans(planPro))

		test.ErrorIs(t, err, ErrEnforcerRequired)
	})

	T.Run("a boolean-only catalog needs no enforcer", func(t *testing.T) {
		t.Parallel()

		c, err := NewPlanChecker(t.Context(), &CheckerConfig{}, newBooleanCatalog(t), staticPlans(planPro),
			WithLogger(loggingnoop.NewLogger()))

		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("rejects a fallback plan the catalog does not define", func(t *testing.T) {
		t.Parallel()

		// Otherwise the misconfiguration is discovered by having the outage it
		// was configured to survive.
		_, err := NewPlanChecker(t.Context(), &CheckerConfig{FallbackPlan: "nope"},
			newBooleanCatalog(t), staticPlans(planPro), WithLogger(loggingnoop.NewLogger()))

		test.ErrorIs(t, err, ErrUnknownPlan)
	})

	T.Run("accepts a fallback plan the catalog defines", func(t *testing.T) {
		t.Parallel()

		_, err := NewPlanChecker(t.Context(), &CheckerConfig{FallbackPlan: planFree},
			newBooleanCatalog(t), staticPlans(planPro), WithLogger(loggingnoop.NewLogger()))

		test.NoError(t, err)
	})

	T.Run("rejects a TTL past the maximum", func(t *testing.T) {
		t.Parallel()

		_, err := NewPlanChecker(t.Context(), &CheckerConfig{CacheTTL: MaxCacheTTL + time.Second},
			newBooleanCatalog(t), staticPlans(planPro))

		test.Error(t, err)
	})

	T.Run("a nil option is ignored", func(t *testing.T) {
		t.Parallel()

		c, err := NewPlanChecker(t.Context(), &CheckerConfig{}, newBooleanCatalog(t), staticPlans(planPro), nil)

		must.NoError(t, err)
		test.NotNil(t, c)
	})
}

func TestPlanChecker_Check_boolean(T *testing.T) {
	T.Parallel()

	T.Run("a plan that includes the feature allows", func(t *testing.T) {
		t.Parallel()

		d, err := newChecker(t, staticPlans(planPro)).Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.True(t, d.Allowed)
		test.EqOp(t, ReasonPlanIncludes, d.Reason)
		test.EqOp(t, planPro, d.Plan)
		test.EqOp(t, KindBoolean, d.Kind)
		test.EqOp(t, authorization.Permission("entitlement.advanced_search"), d.Permission)
		test.NoError(t, d.Err())
	})

	T.Run("a plan that excludes the feature denies", func(t *testing.T) {
		t.Parallel()

		d, err := newChecker(t, staticPlans(planFree)).Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.False(t, d.Allowed)
		test.EqOp(t, ReasonPlanExcludes, d.Reason)
		test.ErrorIs(t, d.Err(), ErrNotEntitled)
	})

	T.Run("quantity fields are unbounded", func(t *testing.T) {
		t.Parallel()

		// A boolean feature has no amount, and rendering "0 remaining" for one is
		// the bug Unbounded exists to make impossible.
		d, err := newChecker(t, staticPlans(planPro)).Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.EqOp(t, Unbounded, d.Limit)
		test.EqOp(t, Unbounded, d.Remaining)
	})

	T.Run("a grant flag opens a feature the plan excludes", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, staticPlans(planFree), WithFeatureFlags(enabledFlags(flagSearchGrant)))

		d, err := c.Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.True(t, d.Allowed)
		test.EqOp(t, ReasonFlagGranted, d.Reason)
	})

	T.Run("a kill flag closes a feature the plan includes", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, staticPlans(planPro), WithFeatureFlags(enabledFlags(flagSearchKill)))

		d, err := c.Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.False(t, d.Allowed)
		test.EqOp(t, ReasonFlagKilled, d.Reason)
	})

	T.Run("a kill flag beats a grant flag", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, staticPlans(planFree),
			WithFeatureFlags(enabledFlags(flagSearchGrant, flagSearchKill)))

		d, err := c.Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.False(t, d.Allowed)
		test.EqOp(t, ReasonFlagKilled, d.Reason)
	})

	T.Run("targeting attributes reach the flag provider", func(t *testing.T) {
		t.Parallel()

		flags, recorded := capturingFlags(flagSearchGrant)
		c := newChecker(t, staticPlans(planFree), WithFeatureFlags(flags))

		d, err := c.Check(t.Context(), testAccount, featureSearch,
			WithTargetingAttributes(map[string]any{"region": "eu-west-1", "beta": true}))

		must.NoError(t, err)
		test.True(t, d.Allowed)

		seen := recorded()
		must.SliceNotEmpty(t, seen)

		for _, evalCtx := range seen {
			// The account stays the targeting key: attributes add signals, they
			// do not replace the subject a provider's percentage rollout hashes.
			test.EqOp(t, testAccount, evalCtx.TargetingKey)
			test.Eq(t, map[string]any{"region": "eu-west-1", "beta": true}, evalCtx.Attributes)
		}
	})

	T.Run("attributes are absent when the caller passes none", func(t *testing.T) {
		t.Parallel()

		flags, recorded := capturingFlags(flagSearchGrant)
		c := newChecker(t, staticPlans(planFree), WithFeatureFlags(flags))

		_, err := c.Check(t.Context(), testAccount, featureSearch)
		must.NoError(t, err)

		seen := recorded()
		must.SliceNotEmpty(t, seen)

		for _, evalCtx := range seen {
			test.Nil(t, evalCtx.Attributes)
		}
	})

	T.Run("an unconfigured flag manager leaves both flags inert", func(t *testing.T) {
		t.Parallel()

		// The plan keeps answering. A deployment with no flag provider is a real
		// configuration, not a degraded one.
		c := newChecker(t, staticPlans(planPro))

		d, err := c.Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.True(t, d.Allowed)
		test.EqOp(t, ReasonPlanIncludes, d.Reason)
	})

	T.Run("a flag provider that errors leaves both flags inert", func(t *testing.T) {
		t.Parallel()

		// The property the whole flag design rests on: a false answer always
		// defers to the plan, so a broken provider cannot revoke an entitlement
		// and cannot grant one.
		errFlags := platformerrors.New("flag provider is down")

		included := newChecker(t, staticPlans(planPro), WithFeatureFlags(failingFlags(errFlags)))
		d, err := included.Check(t.Context(), testAccount, featureSearch)
		must.NoError(t, err)
		test.True(t, d.Allowed)

		excluded := newChecker(t, staticPlans(planFree), WithFeatureFlags(failingFlags(errFlags)))
		d, err = excluded.Check(t.Context(), testAccount, featureSearch)
		must.NoError(t, err)
		test.False(t, d.Allowed)
	})

	T.Run("a flag nobody created is inert on exactly the same terms", func(t *testing.T) {
		t.Parallel()

		// featureflags tells a missing flag apart from a broken provider so that a
		// flag name shipped ahead of its flag does not open a breaker every other
		// flag shares. This package deliberately does not act on the difference:
		// both mean "nobody has told me otherwise", and the plan answers either
		// way. Asserting it here keeps that a decision rather than an oversight.
		included := newChecker(t, staticPlans(planPro), WithFeatureFlags(failingFlags(featureflags.ErrFlagNotFound)))
		d, err := included.Check(t.Context(), testAccount, featureSearch)
		must.NoError(t, err)
		test.True(t, d.Allowed)
		test.EqOp(t, ReasonPlanIncludes, d.Reason)

		excluded := newChecker(t, staticPlans(planFree), WithFeatureFlags(failingFlags(featureflags.ErrFlagNotFound)))
		d, err = excluded.Check(t.Context(), testAccount, featureSearch)
		must.NoError(t, err)
		test.False(t, d.Allowed)
	})
}

func TestPlanChecker_Check_quota(T *testing.T) {
	T.Parallel()

	T.Run("reports what metering decided", func(t *testing.T) {
		t.Parallel()

		resets := time.Now().UTC().Add(time.Hour)
		c := newChecker(t, staticPlans(planPro), WithEnforcer(staticEnforcer(&metering.Decision{
			Allowed:  true,
			Used:     400,
			Limit:    1000,
			ResetsAt: resets,
		}, nil)))

		d, err := c.Check(t.Context(), testAccount, featureTokens)

		must.NoError(t, err)
		test.True(t, d.Allowed)
		test.EqOp(t, ReasonQuotaAvailable, d.Reason)
		test.EqOp(t, int64(400), d.Used)
		test.EqOp(t, int64(1000), d.Limit)
		test.EqOp(t, int64(600), d.Remaining)
		test.EqOp(t, resets, d.ResetsAt)
	})

	T.Run("a spent quota denies with its own sentinel", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, staticPlans(planPro), WithEnforcer(staticEnforcer(&metering.Decision{
			Allowed: false,
			Used:    1000,
			Limit:   1000,
		}, nil)))

		d, err := c.Check(t.Context(), testAccount, featureTokens)

		must.NoError(t, err)
		test.False(t, d.Allowed)
		test.EqOp(t, ReasonQuotaExhausted, d.Reason)
		test.EqOp(t, int64(0), d.Remaining)
		test.ErrorIs(t, d.Err(), ErrQuotaExhausted)
	})

	T.Run("an allowed overage is reported as one", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, staticPlans(planPro), WithEnforcer(staticEnforcer(&metering.Decision{
			Allowed:  true,
			Used:     1200,
			Limit:    1000,
			Overage:  200,
			Behavior: metering.BehaviorAllowOverage,
		}, nil)))

		d, err := c.Check(t.Context(), testAccount, featureTokens)

		must.NoError(t, err)
		test.True(t, d.Allowed)
		test.EqOp(t, ReasonQuotaOverage, d.Reason)
		test.EqOp(t, int64(200), d.Overage)
		test.EqOp(t, int64(0), d.Remaining)
	})

	T.Run("checks for one unit by default", func(t *testing.T) {
		t.Parallel()

		// Not zero. "May I consume nothing" is true at exactly the moment a quota
		// is spent, which is the moment the question is being asked.
		var asked atomic.Int64
		c := newChecker(t, staticPlans(planPro), WithEnforcer(&meteringmock.EnforcerMock{
			CheckFunc: func(_ context.Context, _, _ string, quantity int64) (*metering.Decision, error) {
				asked.Store(quantity)

				return &metering.Decision{Allowed: true, Limit: 1000}, nil
			},
		}))

		_, err := c.Check(t.Context(), testAccount, featureTokens)

		must.NoError(t, err)
		test.EqOp(t, int64(1), asked.Load())
	})

	T.Run("passes an explicit quantity through", func(t *testing.T) {
		t.Parallel()

		var asked atomic.Int64
		c := newChecker(t, staticPlans(planPro), WithEnforcer(&meteringmock.EnforcerMock{
			CheckFunc: func(_ context.Context, _, _ string, quantity int64) (*metering.Decision, error) {
				asked.Store(quantity)

				return &metering.Decision{Allowed: true, Limit: 1000}, nil
			},
		}))

		_, err := c.CheckQuantity(t.Context(), testAccount, featureTokens, 5000)

		must.NoError(t, err)
		test.EqOp(t, int64(5000), asked.Load())
	})

	T.Run("a plan that excludes the feature never reads usage", func(t *testing.T) {
		t.Parallel()

		var calls atomic.Int64
		c := newChecker(t, staticPlans(planFree), WithEnforcer(&meteringmock.EnforcerMock{
			CheckFunc: func(context.Context, string, string, int64) (*metering.Decision, error) {
				calls.Add(1)

				return &metering.Decision{Allowed: true}, nil
			},
		}))

		d, err := c.Check(t.Context(), testAccount, featureTokens)

		must.NoError(t, err)
		test.False(t, d.Allowed)
		test.EqOp(t, ReasonPlanExcludes, d.Reason)
		test.EqOp(t, int64(0), calls.Load())
	})

	T.Run("an unlimited grant short-circuits the usage read", func(t *testing.T) {
		t.Parallel()

		// There is no total that would change the answer, so paying for the read
		// would be spending latency to learn nothing.
		var calls atomic.Int64
		c := newChecker(t, staticPlans(planEnterprise), WithEnforcer(&meteringmock.EnforcerMock{
			CheckFunc: func(context.Context, string, string, int64) (*metering.Decision, error) {
				calls.Add(1)

				return &metering.Decision{Allowed: true}, nil
			},
		}))

		d, err := c.Check(t.Context(), testAccount, featureTokens)

		must.NoError(t, err)
		test.True(t, d.Allowed)
		test.True(t, d.Unlimited)
		test.EqOp(t, ReasonUnlimited, d.Reason)
		test.EqOp(t, Unbounded, d.Remaining)
		test.EqOp(t, int64(0), calls.Load())
	})

	T.Run("a kill flag closes a quota feature without reading usage", func(t *testing.T) {
		t.Parallel()

		var calls atomic.Int64
		c := newChecker(t, staticPlans(planPro),
			WithFeatureFlags(enabledFlags(flagTokensKill)),
			WithEnforcer(&meteringmock.EnforcerMock{
				CheckFunc: func(context.Context, string, string, int64) (*metering.Decision, error) {
					calls.Add(1)

					return &metering.Decision{Allowed: true}, nil
				},
			}))

		d, err := c.Check(t.Context(), testAccount, featureTokens)

		must.NoError(t, err)
		test.False(t, d.Allowed)
		test.EqOp(t, ReasonFlagKilled, d.Reason)
		test.EqOp(t, int64(0), calls.Load())
	})

	T.Run("an enforcer failure is an error, not a denial", func(t *testing.T) {
		t.Parallel()

		// A denial is a claim about the account. A store that cannot be read is
		// not one, and reporting it as a denial would have a customer told they
		// are out of quota during a database outage.
		errCheck := platformerrors.New("metering is down")
		c := newChecker(t, staticPlans(planPro), WithEnforcer(staticEnforcer(nil, errCheck)))

		d, err := c.Check(t.Context(), testAccount, featureTokens)

		test.ErrorIs(t, err, errCheck)
		test.Nil(t, d)
	})

	T.Run("metering staleness propagates to the decision", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, staticPlans(planPro), WithEnforcer(staticEnforcer(&metering.Decision{
			Allowed: true,
			Limit:   1000,
			Stale:   true,
		}, nil)))

		d, err := c.Check(t.Context(), testAccount, featureTokens)

		must.NoError(t, err)
		test.True(t, d.Stale)
	})
}

func TestPlanChecker_Check_planResolution(T *testing.T) {
	T.Parallel()

	T.Run("an account with no plan is denied, not errored", func(t *testing.T) {
		t.Parallel()

		// A request that answers 500 because a customer has not paid is a bug
		// report filed with the wrong team.
		c := newChecker(t, failingPlans(ErrNoPlan))

		d, err := c.Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.False(t, d.Allowed)
		test.EqOp(t, ReasonNoPlan, d.Reason)
	})

	T.Run("an empty plan name is treated as no plan", func(t *testing.T) {
		t.Parallel()

		d, err := newChecker(t, staticPlans("")).Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.EqOp(t, ReasonNoPlan, d.Reason)
	})

	T.Run("a plan the catalog does not define denies distinctly", func(t *testing.T) {
		t.Parallel()

		// Distinct from ReasonNoPlan because it wants a different person woken
		// up: this is a catalog that has drifted from the plan store.
		d, err := newChecker(t, staticPlans("legacy_gold")).Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.False(t, d.Allowed)
		test.EqOp(t, ReasonUnknownPlan, d.Reason)
		test.EqOp(t, "legacy_gold", d.Plan)
	})

	T.Run("a failed resolution with no fallback denies", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, failingPlans(platformerrors.New("plan store is down")))

		d, err := c.Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.False(t, d.Allowed)
		test.EqOp(t, ReasonPlanUnavailable, d.Reason)
	})

	T.Run("a failed resolution falls back to the configured plan", func(t *testing.T) {
		t.Parallel()

		c, err := NewPlanChecker(t.Context(), &CheckerConfig{FallbackPlan: planPro},
			newCatalog(t), failingPlans(platformerrors.New("plan store is down")),
			WithLogger(loggingnoop.NewLogger()),
			WithEnforcer(staticEnforcer(&metering.Decision{Allowed: true, Limit: 1000}, nil)))
		must.NoError(t, err)

		d, err := c.Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.True(t, d.Allowed)
		test.EqOp(t, planPro, d.Plan)
		test.True(t, d.Stale)
	})

	T.Run("the fallback does not apply to an account with no plan", func(t *testing.T) {
		t.Parallel()

		// Not an outage — a customer who has not paid. The fallback is for the
		// former and would quietly hand the product to the latter.
		c, err := NewPlanChecker(t.Context(), &CheckerConfig{FallbackPlan: planPro},
			newCatalog(t), failingPlans(ErrNoPlan),
			WithLogger(loggingnoop.NewLogger()),
			WithEnforcer(staticEnforcer(&metering.Decision{Allowed: true}, nil)))
		must.NoError(t, err)

		d, err := c.Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.False(t, d.Allowed)
		test.EqOp(t, ReasonNoPlan, d.Reason)
	})
}

func TestPlanChecker_Check_inputs(T *testing.T) {
	T.Parallel()

	T.Run("rejects an empty account", func(t *testing.T) {
		t.Parallel()

		_, err := newChecker(t, staticPlans(planPro)).Check(t.Context(), "", featureSearch)

		test.ErrorIs(t, err, ErrEmptyAccount)
	})

	T.Run("an unregistered feature is an error, not a denial", func(t *testing.T) {
		t.Parallel()

		// Answering "your plan does not include it" would have somebody ship a
		// permanently dark feature and open a billing ticket.
		_, err := newChecker(t, staticPlans(planPro)).Check(t.Context(), testAccount, "nope")

		test.ErrorIs(t, err, ErrUnknownFeature)
	})
}

func TestPlanChecker_caching(T *testing.T) {
	T.Parallel()

	T.Run("resolves the plan once across checks", func(t *testing.T) {
		t.Parallel()

		var calls atomic.Int64
		plans := PlanSourceFunc(func(context.Context, string) (string, error) {
			calls.Add(1)

			return planPro, nil
		})

		c := newChecker(t, plans, WithCache(newAssignmentCache(t)))

		for range 3 {
			_, err := c.Check(t.Context(), testAccount, featureSearch)
			must.NoError(t, err)
		}

		test.EqOp(t, int64(1), calls.Load())
	})

	T.Run("a cached answer is marked stale", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, staticPlans(planPro), WithCache(newAssignmentCache(t)))

		first, err := c.Check(t.Context(), testAccount, featureSearch)
		must.NoError(t, err)
		test.False(t, first.Stale)

		second, err := c.Check(t.Context(), testAccount, featureSearch)
		must.NoError(t, err)
		test.True(t, second.Stale)
	})

	T.Run("Invalidate drops the entry", func(t *testing.T) {
		t.Parallel()

		var calls atomic.Int64
		plans := PlanSourceFunc(func(context.Context, string) (string, error) {
			calls.Add(1)

			return planPro, nil
		})

		c := newChecker(t, plans, WithCache(newAssignmentCache(t)))

		_, err := c.Check(t.Context(), testAccount, featureSearch)
		must.NoError(t, err)

		must.NoError(t, c.Invalidate(t.Context(), testAccount))

		_, err = c.Check(t.Context(), testAccount, featureSearch)
		must.NoError(t, err)

		test.EqOp(t, int64(2), calls.Load())
	})

	T.Run("Invalidate without a cache is a no-op", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, newChecker(t, staticPlans(planPro)).Invalidate(t.Context(), testAccount))
	})

	T.Run("Invalidate reports a delete that failed", func(t *testing.T) {
		t.Parallel()

		// Unlike a read fault, this one is returned: the caller asked for stale
		// policy to stop being served, and silently not doing that is the bug
		// they were trying to avoid.
		errDelete := platformerrors.New("cache is down")
		c := newChecker(t, staticPlans(planPro), WithCache(&cachemock.CacheMock[Assignment]{
			DeleteFunc: func(context.Context, string) error { return errDelete },
		}))

		test.ErrorIs(t, c.Invalidate(t.Context(), testAccount), errDelete)
	})

	T.Run("a read fault degrades to the plan source", func(t *testing.T) {
		t.Parallel()

		// The wrong response to a degraded cache is to stop answering.
		c := newChecker(t, staticPlans(planPro), WithCache(&cachemock.CacheMock[Assignment]{
			GetFunc: func(context.Context, string) (*Assignment, error) {
				return nil, cache.ErrUnavailable
			},
			SetFunc: func(context.Context, string, *Assignment, ...cache.WriteOption) error {
				return cache.ErrUnavailable
			},
		}))

		d, err := c.Check(t.Context(), testAccount, featureSearch)

		must.NoError(t, err)
		test.True(t, d.Allowed)
		test.False(t, d.Stale)
	})

	T.Run("a failed resolution is not cached", func(t *testing.T) {
		t.Parallel()

		// Caching the fallback would extend a momentary blip into a full TTL of
		// every account being on the fallback tier.
		var calls atomic.Int64
		plans := PlanSourceFunc(func(context.Context, string) (string, error) {
			calls.Add(1)

			return "", platformerrors.New("plan store is down")
		})

		c, err := NewPlanChecker(t.Context(), &CheckerConfig{FallbackPlan: planPro},
			newCatalog(t), plans,
			WithLogger(loggingnoop.NewLogger()),
			WithCache(newAssignmentCache(t)),
			WithEnforcer(staticEnforcer(&metering.Decision{Allowed: true, Limit: 1000}, nil)))
		must.NoError(t, err)

		for range 2 {
			_, checkErr := c.Check(t.Context(), testAccount, featureSearch)
			must.NoError(t, checkErr)
		}

		test.EqOp(t, int64(2), calls.Load())
	})

	T.Run("keys are namespaced by the configured prefix", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, staticPlans(planPro))

		test.EqOp(t, DefaultCachePrefix+testAccount, c.cacheKey(testAccount))
	})
}

func TestPlanChecker_Permissions(T *testing.T) {
	T.Parallel()

	T.Run("carries the plan's boolean features", func(t *testing.T) {
		t.Parallel()

		set, err := newChecker(t, staticPlans(planPro)).Permissions(t.Context(), testAccount)

		must.NoError(t, err)
		test.True(t, set.Has("entitlement.advanced_search"))
	})

	T.Run("carries targeting attributes to every flag it evaluates", func(t *testing.T) {
		t.Parallel()

		// Permissions evaluates both flags of every boolean feature in one call,
		// so an attribute that reached only the first would produce a set that is
		// internally inconsistent about the same account.
		flags, recorded := capturingFlags()
		c := newChecker(t, staticPlans(planPro), WithFeatureFlags(flags))

		_, err := c.Permissions(t.Context(), testAccount,
			WithTargetingAttributes(map[string]any{"region": "eu-west-1"}))
		must.NoError(t, err)

		seen := recorded()
		must.SliceNotEmpty(t, seen)

		for _, evalCtx := range seen {
			test.EqOp(t, testAccount, evalCtx.TargetingKey)
			test.Eq(t, map[string]any{"region": "eu-west-1"}, evalCtx.Attributes)
		}
	})

	T.Run("omits quota features", func(t *testing.T) {
		t.Parallel()

		// A permission meaning "was entitled a moment ago" would be read by every
		// caller as "may proceed".
		set, err := newChecker(t, staticPlans(planPro)).Permissions(t.Context(), testAccount)

		must.NoError(t, err)
		test.False(t, set.Has("entitlement.llm_tokens"))
		test.EqOp(t, 1, set.Len())
	})

	T.Run("a plan that includes nothing carries nothing", func(t *testing.T) {
		t.Parallel()

		set, err := newChecker(t, staticPlans(planFree)).Permissions(t.Context(), testAccount)

		must.NoError(t, err)
		test.True(t, set.IsEmpty())
	})

	T.Run("a grant flag adds a permission", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, staticPlans(planFree), WithFeatureFlags(enabledFlags(flagSearchGrant)))

		set, err := c.Permissions(t.Context(), testAccount)

		must.NoError(t, err)
		test.True(t, set.Has("entitlement.advanced_search"))
	})

	T.Run("a kill flag removes one", func(t *testing.T) {
		t.Parallel()

		c := newChecker(t, staticPlans(planPro), WithFeatureFlags(enabledFlags(flagSearchKill)))

		set, err := c.Permissions(t.Context(), testAccount)

		must.NoError(t, err)
		test.True(t, set.IsEmpty())
	})

	T.Run("an unresolvable plan is the empty set, not an error", func(t *testing.T) {
		t.Parallel()

		// This runs while building a session. Failing it logs the holder out of a
		// product they are still entitled to most of.
		for _, plans := range []PlanSource{
			failingPlans(ErrNoPlan),
			failingPlans(platformerrors.New("plan store is down")),
			staticPlans("legacy_gold"),
		} {
			set, err := newChecker(t, plans).Permissions(t.Context(), testAccount)

			must.NoError(t, err)
			test.True(t, set.IsEmpty())
		}
	})

	T.Run("rejects an empty account", func(t *testing.T) {
		t.Parallel()

		_, err := newChecker(t, staticPlans(planPro)).Permissions(t.Context(), "")

		test.ErrorIs(t, err, ErrEmptyAccount)
	})

	T.Run("composes with role permissions", func(t *testing.T) {
		t.Parallel()

		// The shape the whole Permissions method exists for: one grants.Has, and
		// the handler does not know which of the two put the permission there.
		entitlementPerms, err := newChecker(t, staticPlans(planPro)).Permissions(t.Context(), testAccount)
		must.NoError(t, err)

		rolePerms := authorization.NewPermissionSet("update.recipes")
		grants := authorization.NewGrants(rolePerms, entitlementPerms)

		test.True(t, grants.Has("update.recipes"))
		test.True(t, grants.Has("entitlement.advanced_search"))
		test.False(t, grants.Has("entitlement.llm_tokens"))
	})
}
