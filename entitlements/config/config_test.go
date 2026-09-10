package entitlementscfg

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/entitlements"
	"github.com/primandproper/platform-go/v14/metering"

	"github.com/primandproper/primitives-go/cache"
	"github.com/primandproper/primitives-go/cache/memory"
	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/featureflags"
	featureflagsmock "github.com/primandproper/primitives-go/featureflags/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const testMeter = "llm_tokens"

// testFeatures are the code-declared features every test here registers.
func testFeatures() []entitlements.Feature {
	return []entitlements.Feature{
		{Key: "advanced_search", Kind: entitlements.KindBoolean},
		{Key: "llm_tokens", Kind: entitlements.KindQuota, Meter: testMeter},
	}
}

// testPlans are the configured tiers.
func testPlans() []entitlements.Plan {
	return []entitlements.Plan{
		{Name: "free"},
		{Name: "pro", Includes: []entitlements.Grant{
			{Feature: "advanced_search"},
			{Feature: "llm_tokens", Limit: 1000},
		}},
	}
}

func testRegistry(t *testing.T) *metering.Registry {
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

// newAssignmentCache builds the in-memory cache the read-through test seeds.
func newAssignmentCache(tb testing.TB) cache.Cache[entitlements.Assignment] {
	tb.Helper()

	c, err := memory.NewInMemoryCache[entitlements.Assignment](time.Minute)
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = c.Close() })

	return c
}

func testEnforcer() metering.Enforcer {
	return &stubEnforcer{}
}

type stubEnforcer struct{}

func (stubEnforcer) Check(context.Context, string, string, int64) (*metering.Decision, error) {
	return &metering.Decision{Allowed: true}, nil
}

func (stubEnforcer) Consume(context.Context, database.Tx, string, string, int64) (*metering.Decision, error) {
	return &metering.Decision{Allowed: true}, nil
}

func (stubEnforcer) ConsumeUsage(context.Context, database.Tx, metering.Usage) (*metering.Decision, error) {
	return &metering.Decision{Allowed: true}, nil
}

func TestConfig(T *testing.T) {
	T.Parallel()

	T.Run("EnsureDefaults reaches the nested checker config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, entitlements.DefaultCacheTTL, cfg.Checker.CacheTTL)
	})

	T.Run("validates the nested checker config", func(t *testing.T) {
		t.Parallel()

		// ozzo dereferences a struct-value field before checking
		// ValidatableWithContext, so this is the assertion that the By closure is
		// doing its job.
		cfg := &Config{Checker: entitlements.CheckerConfig{CacheTTL: entitlements.MaxCacheTTL + time.Second}}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("a defaulted config validates", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewCatalog(T *testing.T) {
	T.Parallel()

	T.Run("registers code features and configured plans", func(t *testing.T) {
		t.Parallel()

		catalog, err := NewCatalog(t.Context(), &Config{Plans: testPlans()}, testFeatures())
		must.NoError(t, err)

		test.Eq(t, []string{"advanced_search", "llm_tokens"}, catalog.FeatureKeys())
		test.Eq(t, []string{"free", "pro"}, catalog.PlanNames())

		g, ok := catalog.GrantFor("pro", "llm_tokens")
		must.True(t, ok)
		test.EqOp(t, int64(1000), g.Limit)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewCatalog(t.Context(), nil, testFeatures())

		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("reports a plan naming an unknown feature", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Plans: []entitlements.Plan{
			{Name: "pro", Includes: []entitlements.Grant{{Feature: "typo"}}},
		}}

		_, err := NewCatalog(t.Context(), cfg, testFeatures())

		test.ErrorIs(t, err, entitlements.ErrUnknownFeature)
	})

	T.Run("reports a bad feature declaration", func(t *testing.T) {
		t.Parallel()

		_, err := NewCatalog(t.Context(), &Config{}, []entitlements.Feature{{Key: "no kind"}})

		test.ErrorIs(t, err, entitlements.ErrInvalidFeatureKey)
	})

	T.Run("an empty config builds an empty catalog", func(t *testing.T) {
		t.Parallel()

		catalog, err := NewCatalog(t.Context(), &Config{}, nil)

		must.NoError(t, err)
		test.SliceEmpty(t, catalog.FeatureKeys())
		test.SliceEmpty(t, catalog.PlanNames())
	})
}

func TestNewChecker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		catalog, err := NewCatalog(t.Context(), &Config{Plans: testPlans()}, testFeatures())
		must.NoError(t, err)

		checker, err := NewChecker(t.Context(), &Config{}, catalog,
			entitlements.NewStaticPlanSource("pro"), WithEnforcer(testEnforcer()))
		must.NoError(t, err)

		d, err := checker.Check(t.Context(), "account_123", "advanced_search")
		must.NoError(t, err)
		test.True(t, d.Allowed)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewChecker(t.Context(), nil, entitlements.NewCatalog(),
			entitlements.NewStaticPlanSource("pro"))

		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("reports a quota catalog with no enforcer", func(t *testing.T) {
		t.Parallel()

		catalog, err := NewCatalog(t.Context(), &Config{Plans: testPlans()}, testFeatures())
		must.NoError(t, err)

		_, err = NewChecker(t.Context(), &Config{}, catalog,
			entitlements.NewStaticPlanSource("pro"))

		test.ErrorIs(t, err, entitlements.ErrEnforcerRequired)
	})

	T.Run("an enforcer supplied by option satisfies a quota catalog", func(t *testing.T) {
		t.Parallel()

		catalog, err := NewCatalog(t.Context(), &Config{Plans: testPlans()}, testFeatures())
		must.NoError(t, err)

		checker, err := NewChecker(t.Context(), &Config{}, catalog,
			entitlements.NewStaticPlanSource("pro"), WithEnforcer(testEnforcer()))

		must.NoError(t, err)
		test.NotNil(t, checker)
	})

	T.Run("the flag manager supplied by option reaches the checker", func(t *testing.T) {
		t.Parallel()

		// advanced_search is not in the free plan, so the only way to be
		// allowed it is through the grant flag — which is only consulted if the
		// option arrived.
		features := []entitlements.Feature{
			{Key: "advanced_search", Kind: entitlements.KindBoolean, GrantFlag: "advanced_search_grant"},
		}

		catalog, err := NewCatalog(t.Context(),
			&Config{Plans: []entitlements.Plan{{Name: "free"}}}, features)
		must.NoError(t, err)

		checker, err := NewChecker(t.Context(), &Config{}, catalog,
			entitlements.NewStaticPlanSource("free"),
			WithFeatureFlags(enabledFlags("advanced_search_grant")))
		must.NoError(t, err)

		d, err := checker.Check(t.Context(), "account_123", "advanced_search")
		must.NoError(t, err)
		test.True(t, d.Allowed)
	})

	T.Run("the assignment cache supplied by option reaches the checker", func(t *testing.T) {
		t.Parallel()

		catalog, err := NewCatalog(t.Context(), &Config{Plans: testPlans()}, testFeatures())
		must.NoError(t, err)

		// The plan source says free and the cache says pro. A checker that
		// received the cache answers from it.
		assignments := newAssignmentCache(t)
		must.NoError(t, assignments.Set(t.Context(),
			entitlements.DefaultCachePrefix+"account_123", &entitlements.Assignment{Plan: "pro"}))

		checker, err := NewChecker(t.Context(), &Config{}, catalog,
			entitlements.NewStaticPlanSource("free"),
			WithEnforcer(testEnforcer()), WithAssignmentCache(assignments))
		must.NoError(t, err)

		d, err := checker.Check(t.Context(), "account_123", "advanced_search")
		must.NoError(t, err)
		test.True(t, d.Allowed)
	})

	T.Run("passes explicit options through after the derived ones", func(t *testing.T) {
		t.Parallel()

		catalog, err := NewCatalog(t.Context(), &Config{Plans: testPlans()}, testFeatures())
		must.NoError(t, err)

		// No WithEnforcer, so only the passthrough option can satisfy the quota
		// catalog — which is what pins that the passthrough is applied at all.
		checker, err := NewChecker(t.Context(), &Config{}, catalog,
			entitlements.NewStaticPlanSource("pro"),
			WithCheckerOptions(entitlements.WithEnforcer(testEnforcer())))

		must.NoError(t, err)
		test.NotNil(t, checker)
	})
}

func TestNewQuotaSource(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		catalog, err := NewCatalog(t.Context(), &Config{Plans: testPlans()}, testFeatures())
		must.NoError(t, err)

		quotas, err := NewQuotaSource(t.Context(), &Config{}, catalog,
			entitlements.NewStaticPlanSource("pro"), testRegistry(t))
		must.NoError(t, err)

		q, err := quotas.QuotaFor(t.Context(), "account_123", testMeter)
		must.NoError(t, err)
		test.EqOp(t, int64(1000), q.Limit)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewQuotaSource(t.Context(), nil, entitlements.NewCatalog(),
			entitlements.NewStaticPlanSource("pro"), testRegistry(t))

		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("reports a catalog whose meter the registry lacks", func(t *testing.T) {
		t.Parallel()

		catalog, err := NewCatalog(t.Context(), &Config{}, testFeatures())
		must.NoError(t, err)

		_, err = NewQuotaSource(t.Context(), &Config{}, catalog,
			entitlements.NewStaticPlanSource("pro"), metering.NewRegistry())

		test.ErrorIs(t, err, metering.ErrUnknownMeter)
	})
}
