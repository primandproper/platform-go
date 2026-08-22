package entitlements

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/featureflags"
	featureflagsmock "github.com/primandproper/platform-go/v13/featureflags/mock"
	"github.com/primandproper/platform-go/v13/metering"
	meteringmock "github.com/primandproper/platform-go/v13/metering/mock"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"

	"github.com/shoenig/test/must"
)

// The fixture vocabulary every test in this package shares. Named rather than
// repeated because the strings are load-bearing in three different ways — a
// feature key is validated as an identifier, a flag name is not, and a plan name
// is what the catalog indexes grants by — and a typo in a literal produces a
// test that passes for the wrong reason: an unregistered feature and a
// misspelled one are the same string to the catalog.
const (
	testAccount = "account_123"

	// featureSearch is the boolean feature; featureTokens is the quota one.
	featureSearch = "advanced_search"
	featureTokens = "llm_tokens"

	// testMeter is the meter featureTokens counts against. It matches the
	// feature key because that is the ordinary way to name one, not because
	// anything requires it — QuotaSource maps the two through Feature.Meter.
	testMeter = featureTokens

	// Flag names are hyphenated where feature keys are not: they are provider
	// identifiers, not this package's, and nothing validates them here.
	flagSearchGrant = "advanced-search-rollout"
	flagSearchKill  = "advanced-search-kill"
	flagTokensKill  = "llm-tokens-kill"

	planPro        = "pro"
	planEnterprise = "enterprise"
	planFree       = "free"
)

// newRegistry builds a metering registry holding the meter the quota feature in
// these tests counts against.
func newRegistry(tb testing.TB) *metering.Registry {
	tb.Helper()

	r := metering.NewRegistry()
	must.NoError(tb, r.RegisterMeter(metering.Meter{
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
func newCatalog(tb testing.TB) *Catalog {
	tb.Helper()

	c := NewCatalog()
	must.NoError(tb, c.RegisterFeature(Feature{
		Key:       featureSearch,
		Kind:      KindBoolean,
		GrantFlag: flagSearchGrant,
		KillFlag:  flagSearchKill,
	}))
	must.NoError(tb, c.RegisterFeature(Feature{
		Key:      featureTokens,
		Kind:     KindQuota,
		Meter:    testMeter,
		KillFlag: flagTokensKill,
	}))

	must.NoError(tb, c.RegisterPlan(Plan{
		Name: planPro,
		Includes: []Grant{
			{Feature: featureSearch},
			{Feature: featureTokens, Limit: 1000},
		},
	}))
	must.NoError(tb, c.RegisterPlan(Plan{
		Name:     planEnterprise,
		Includes: []Grant{{Feature: featureSearch}, {Feature: featureTokens, Unlimited: true}},
	}))
	must.NoError(tb, c.RegisterPlan(Plan{Name: planFree}))

	return c
}

// newBooleanCatalog builds a catalog with no quota features, for the tests that
// assert a Checker needs no enforcer without them.
func newBooleanCatalog(tb testing.TB) *Catalog {
	tb.Helper()

	c := NewCatalog()
	must.NoError(tb, c.RegisterFeature(Feature{Key: featureSearch, Kind: KindBoolean}))
	must.NoError(tb, c.RegisterPlan(Plan{Name: planPro, Includes: []Grant{{Feature: featureSearch}}}))
	must.NoError(tb, c.RegisterPlan(Plan{Name: planFree}))

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

// capturingFlags records the evaluation context of every flag it is asked
// about, so a test can assert on what reached the provider rather than only on
// the decision that came back. It reports true for the named flags, as
// enabledFlags does.
//
// The recorded contexts are returned by the accessor rather than exposed as a
// field, because a checker evaluates flags on the caller's goroutine and the
// race detector is the point of running these with -race.
func capturingFlags(enabled ...string) (
	manager featureflags.FeatureFlagManager,
	recorded func() []featureflags.EvaluationContext,
) {
	on := make(map[string]struct{}, len(enabled))
	for _, f := range enabled {
		on[f] = struct{}{}
	}

	var (
		mu   sync.Mutex
		seen []featureflags.EvaluationContext
	)

	manager = &featureflagsmock.FeatureFlagManagerMock{
		CanUseFeatureFunc: func(_ context.Context, feature string, evalCtx featureflags.EvaluationContext) (bool, error) {
			mu.Lock()
			seen = append(seen, evalCtx)
			mu.Unlock()

			_, ok := on[feature]

			return ok, nil
		},
		CloseFunc: func() error { return nil },
	}

	return manager, func() []featureflags.EvaluationContext {
		mu.Lock()
		defer mu.Unlock()

		return slices.Clone(seen)
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
func newAssignmentCache(tb testing.TB) cache.Cache[Assignment] {
	tb.Helper()

	c, err := memory.NewInMemoryCache[Assignment](time.Minute)
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = c.Close() })

	return c
}

// newChecker builds a checker over the standard catalog with a noop logger, so a
// test that trips the "no cache" or "fault" paths does not print to the suite's
// output.
func newChecker(tb testing.TB, plans PlanSource, opts ...CheckerOption) *PlanChecker {
	tb.Helper()

	base := []CheckerOption{
		WithLogger(loggingnoop.NewLogger()),
		WithEnforcer(staticEnforcer(&metering.Decision{Allowed: true, Limit: 1000}, nil)),
	}

	c, err := NewPlanChecker(tb.Context(), &CheckerConfig{}, newCatalog(tb), plans, append(base, opts...)...)
	must.NoError(tb, err)

	return c
}
