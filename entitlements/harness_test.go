package entitlements

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v10/cache"
	"github.com/primandproper/platform-go/v10/cache/memory"
	"github.com/primandproper/platform-go/v10/featureflags"
	featureflagsmock "github.com/primandproper/platform-go/v10/featureflags/mock"
	"github.com/primandproper/platform-go/v10/metering"
	meteringmock "github.com/primandproper/platform-go/v10/metering/mock"
	loggingnoop "github.com/primandproper/platform-go/v10/observability/logging/noop"

	"github.com/shoenig/test/must"
)

const (
	testAccount = "account_123"
	testMeter   = "llm_tokens"
)

// newRegistry builds a metering registry holding the meter the quota feature in
// these tests counts against.
func newRegistry(t *testing.T) *metering.Registry {
	t.Helper()

	r := metering.NewRegistry()
	must.NoError(t, r.RegisterMeter(metering.Meter{
		Name:        testMeter,
		Unit:        "tokens",
		Aggregation: metering.AggregationSum,
		Period:      metering.PeriodMonth,
	}))

	return r
}

// newCatalog builds the catalog every checker test runs against: one boolean
// feature with both flags, one quota feature with a kill flag, and three plans —
// one that includes everything with a limit, one that includes the quota feature
// without one, and one that includes nothing.
func newCatalog(t *testing.T) *Catalog {
	t.Helper()

	c := NewCatalog()
	must.NoError(t, c.RegisterFeature(Feature{
		Key:       "advanced_search",
		Kind:      KindBoolean,
		GrantFlag: "advanced-search-rollout",
		KillFlag:  "advanced-search-kill",
	}))
	must.NoError(t, c.RegisterFeature(Feature{
		Key:      "llm_tokens",
		Kind:     KindQuota,
		Meter:    testMeter,
		KillFlag: "llm-tokens-kill",
	}))

	must.NoError(t, c.RegisterPlan(Plan{
		Name: "pro",
		Includes: []Grant{
			{Feature: "advanced_search"},
			{Feature: "llm_tokens", Limit: 1000},
		},
	}))
	must.NoError(t, c.RegisterPlan(Plan{
		Name:     "enterprise",
		Includes: []Grant{{Feature: "advanced_search"}, {Feature: "llm_tokens", Unlimited: true}},
	}))
	must.NoError(t, c.RegisterPlan(Plan{Name: "free"}))

	return c
}

// newBooleanCatalog builds a catalog with no quota features, for the tests that
// assert a Checker needs no enforcer without them.
func newBooleanCatalog(t *testing.T) *Catalog {
	t.Helper()

	c := NewCatalog()
	must.NoError(t, c.RegisterFeature(Feature{Key: "advanced_search", Kind: KindBoolean}))
	must.NoError(t, c.RegisterPlan(Plan{Name: "pro", Includes: []Grant{{Feature: "advanced_search"}}}))
	must.NoError(t, c.RegisterPlan(Plan{Name: "free"}))

	return c
}

// staticPlans puts every account on one plan.
func staticPlans(plan string) PlanSource {
	return PlanSourceFunc(func(context.Context, string) (string, error) {
		return plan, nil
	})
}

// failingPlans reports err for every account.
func failingPlans(err error) PlanSource {
	return PlanSourceFunc(func(context.Context, string) (string, error) {
		return "", err
	})
}

// enabledFlags reports true for exactly the named flags and false for the rest,
// which is what every real provider does for a flag it has never heard of.
func enabledFlags(enabled ...string) featureflags.FeatureFlagManager {
	on := make(map[string]struct{}, len(enabled))
	for _, f := range enabled {
		on[f] = struct{}{}
	}

	return &featureflagsmock.FeatureFlagManagerMock{
		CanUseFeatureFunc: func(_ context.Context, feature string, _ featureflags.EvaluationContext) (bool, error) {
			_, ok := on[feature]

			return ok, nil
		},
		CloseFunc: func() error { return nil },
	}
}

// failingFlags reports err for every evaluation.
func failingFlags(err error) featureflags.FeatureFlagManager {
	return &featureflagsmock.FeatureFlagManagerMock{
		CanUseFeatureFunc: func(context.Context, string, featureflags.EvaluationContext) (bool, error) {
			return false, err
		},
		CloseFunc: func() error { return nil },
	}
}

// staticEnforcer answers every Check with decision.
func staticEnforcer(decision *metering.Decision, err error) metering.Enforcer {
	return &meteringmock.EnforcerMock{
		CheckFunc: func(context.Context, string, string, int64) (*metering.Decision, error) {
			return decision, err
		},
	}
}

// newAssignmentCache builds the in-memory cache the read-through tests use.
func newAssignmentCache(t *testing.T) cache.Cache[Assignment] {
	t.Helper()

	c, err := memory.NewInMemoryCache[Assignment](time.Minute)
	must.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	return c
}

// newChecker builds a checker over the standard catalog with a noop logger, so a
// test that trips the "no cache" or "fault" paths does not print to the suite's
// output.
func newChecker(t *testing.T, plans PlanSource, opts ...CheckerOption) *PlanChecker {
	t.Helper()

	base := []CheckerOption{
		WithLogger(loggingnoop.NewLogger()),
		WithEnforcer(staticEnforcer(&metering.Decision{Allowed: true, Limit: 1000}, nil)),
	}

	c, err := NewPlanChecker(t.Context(), &CheckerConfig{}, newCatalog(t), plans, append(base, opts...)...)
	must.NoError(t, err)

	return c
}
