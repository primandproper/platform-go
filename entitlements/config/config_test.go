package entitlementscfg

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/entitlements"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/metering"

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

func testEnforcer() metering.Enforcer {
	return &stubEnforcer{}
}

type stubEnforcer struct{}

func (stubEnforcer) Check(context.Context, string, string, int64) (*metering.Decision, error) {
	return &metering.Decision{Allowed: true}, nil
}

func (stubEnforcer) Consume(context.Context, string, string, int64) (*metering.Decision, error) {
	return &metering.Decision{Allowed: true}, nil
}

func (stubEnforcer) ConsumeUsage(context.Context, metering.Usage) (*metering.Decision, error) {
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
			entitlements.NewStaticPlanSource("pro"), testEnforcer(), nil, nil)
		must.NoError(t, err)

		d, err := checker.Check(t.Context(), "account_123", "advanced_search")
		must.NoError(t, err)
		test.True(t, d.Allowed)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewChecker(t.Context(), nil, entitlements.NewCatalog(),
			entitlements.NewStaticPlanSource("pro"), nil, nil, nil)

		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("reports a quota catalog with no enforcer", func(t *testing.T) {
		t.Parallel()

		catalog, err := NewCatalog(t.Context(), &Config{Plans: testPlans()}, testFeatures())
		must.NoError(t, err)

		_, err = NewChecker(t.Context(), &Config{}, catalog,
			entitlements.NewStaticPlanSource("pro"), nil, nil, nil)

		test.ErrorIs(t, err, entitlements.ErrEnforcerRequired)
	})

	T.Run("passes explicit options through after the derived ones", func(t *testing.T) {
		t.Parallel()

		catalog, err := NewCatalog(t.Context(), &Config{Plans: testPlans()}, testFeatures())
		must.NoError(t, err)

		// The enforcer positional is nil, so only the passthrough option can
		// satisfy the quota catalog.
		checker, err := NewChecker(t.Context(), &Config{}, catalog,
			entitlements.NewStaticPlanSource("pro"), nil, nil, nil,
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
