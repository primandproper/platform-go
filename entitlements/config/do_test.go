package entitlementscfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v10/cache"
	"github.com/primandproper/platform-go/v10/cache/memory"
	"github.com/primandproper/platform-go/v10/entitlements"
	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/featureflags"
	featureflagsnoop "github.com/primandproper/platform-go/v10/featureflags/noop"
	"github.com/primandproper/platform-go/v10/metering"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// baseInjector registers what every registration here needs.
func baseInjector(t *testing.T) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue(i, &Config{Plans: testPlans()})
	do.ProvideValue(i, testFeatures())
	do.ProvideValue[entitlements.PlanSource](i, entitlements.NewStaticPlanSource("pro"))

	return i
}

func TestRegisterCatalog(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := baseInjector(t)
		RegisterCatalog(i)

		catalog, err := do.Invoke[*entitlements.Catalog](i)
		must.NoError(t, err)
		test.Eq(t, []string{"free", "pro"}, catalog.PlanNames())
	})
}

func TestRegisterQuotaSource(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := baseInjector(t)
		do.ProvideValue(i, testRegistry(t))
		RegisterCatalog(i)
		RegisterQuotaSource(i)

		quotas, err := do.Invoke[*entitlements.QuotaSource](i)
		must.NoError(t, err)

		q, err := quotas.QuotaFor(t.Context(), "account_123", testMeter)
		must.NoError(t, err)
		test.EqOp(t, int64(1000), q.Limit)
	})
}

func TestRegisterChecker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := baseInjector(t)
		do.ProvideValue[metering.Enforcer](i, testEnforcer())
		RegisterCatalog(i)
		RegisterChecker(i)

		checker, err := do.Invoke[entitlements.Checker](i)
		must.NoError(t, err)

		d, err := checker.Check(t.Context(), "account_123", "advanced_search")
		must.NoError(t, err)
		test.True(t, d.Allowed)
	})

	T.Run("resolves the optional dependencies when they are registered", func(t *testing.T) {
		t.Parallel()

		assignments, err := memory.NewInMemoryCache[entitlements.Assignment](0)
		must.NoError(t, err)
		t.Cleanup(func() { _ = assignments.Close() })

		i := baseInjector(t)
		do.ProvideValue[metering.Enforcer](i, testEnforcer())
		do.ProvideValue[featureflags.FeatureFlagManager](i, featureflagsnoop.NewFeatureFlagManager())
		do.ProvideValue[cache.Cache[entitlements.Assignment]](i, assignments)
		RegisterCatalog(i)
		RegisterChecker(i)

		checker, err := do.Invoke[entitlements.Checker](i)
		must.NoError(t, err)
		test.NotNil(t, checker)
	})

	T.Run("an unregistered enforcer fails a quota catalog", func(t *testing.T) {
		t.Parallel()

		// Absent is fine for a boolean catalog and an error for this one — the
		// distinction InvokeOptional exists to preserve, reported by the
		// constructor rather than swallowed by the container.
		i := baseInjector(t)
		RegisterCatalog(i)
		RegisterChecker(i)

		_, err := do.Invoke[entitlements.Checker](i)

		test.ErrorIs(t, err, entitlements.ErrEnforcerRequired)
	})

	T.Run("a registered dependency that fails to build is an error", func(t *testing.T) {
		t.Parallel()

		errBuild := platformerrors.New("building the flag manager")

		i := baseInjector(t)
		do.ProvideValue[metering.Enforcer](i, testEnforcer())
		do.Provide(i, func(do.Injector) (featureflags.FeatureFlagManager, error) {
			return nil, errBuild
		})
		RegisterCatalog(i)
		RegisterChecker(i)

		_, err := do.Invoke[entitlements.Checker](i)

		test.ErrorIs(t, err, errBuild)
	})
}
