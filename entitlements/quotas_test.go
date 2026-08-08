package entitlements

import (
	"testing"

	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/metering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newQuotaSource builds a quota source over the standard catalog.
func newQuotaSource(t *testing.T, plans PlanSource) *QuotaSource {
	t.Helper()

	q, err := NewQuotaSource(newCatalog(t), plans, newRegistry(t))
	must.NoError(t, err)

	return q
}

func TestNewQuotaSource(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, newQuotaSource(t, staticPlans("pro")))
	})

	T.Run("rejects nil dependencies", func(t *testing.T) {
		t.Parallel()

		_, err := NewQuotaSource(nil, staticPlans("pro"), newRegistry(t))
		test.ErrorIs(t, err, ErrNilCatalog)

		_, err = NewQuotaSource(newCatalog(t), nil, newRegistry(t))
		test.ErrorIs(t, err, ErrNilPlanSource)

		_, err = NewQuotaSource(newCatalog(t), staticPlans("pro"), nil)
		test.ErrorIs(t, err, ErrNilRegistry)
	})

	T.Run("rejects a catalog naming a meter the registry does not have", func(t *testing.T) {
		t.Parallel()

		// The failure this catches presents as a Check that errors for one
		// feature and works for the rest.
		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(Feature{Key: "seats", Kind: KindQuota, Meter: "seats"}))

		_, err := NewQuotaSource(c, staticPlans("pro"), newRegistry(t))

		test.ErrorIs(t, err, metering.ErrUnknownMeter)
	})
}

func TestQuotaSource_QuotaFor(T *testing.T) {
	T.Parallel()

	T.Run("serves the plan's limit", func(t *testing.T) {
		t.Parallel()

		q, err := newQuotaSource(t, staticPlans("pro")).QuotaFor(t.Context(), testAccount, testMeter)

		must.NoError(t, err)
		test.EqOp(t, int64(1000), q.Limit)
		test.EqOp(t, metering.BehaviorBlock, q.Behavior)
	})

	T.Run("derives the period from the meter", func(t *testing.T) {
		t.Parallel()

		// Taking it instead would move metering's ErrPeriodMismatch — a
		// wiring-time mistake it can only raise on the request path — back onto
		// the request path.
		q, err := newQuotaSource(t, staticPlans("pro")).QuotaFor(t.Context(), testAccount, testMeter)

		must.NoError(t, err)
		test.EqOp(t, metering.PeriodMonth, q.Period)
	})

	T.Run("an unlimited grant becomes a limit nobody reaches", func(t *testing.T) {
		t.Parallel()

		q, err := newQuotaSource(t, staticPlans("enterprise")).QuotaFor(t.Context(), testAccount, testMeter)

		must.NoError(t, err)
		test.EqOp(t, UnlimitedLimit, q.Limit)
		test.EqOp(t, metering.BehaviorAllowOverage, q.Behavior)
	})

	T.Run("a plan that excludes the feature has no quota", func(t *testing.T) {
		t.Parallel()

		// Reported rather than treated as unlimited — see metering.ErrNoQuota.
		_, err := newQuotaSource(t, staticPlans("free")).QuotaFor(t.Context(), testAccount, testMeter)

		test.ErrorIs(t, err, metering.ErrNoQuota)
	})

	T.Run("a meter no feature names has no quota", func(t *testing.T) {
		t.Parallel()

		registry := newRegistry(t)
		must.NoError(t, registry.RegisterMeter(metering.Meter{
			Name:        "unmetered_by_catalog",
			Unit:        "things",
			Aggregation: metering.AggregationSum,
			Period:      metering.PeriodMonth,
		}))

		q, err := NewQuotaSource(newCatalog(t), staticPlans("pro"), registry)
		must.NoError(t, err)

		_, err = q.QuotaFor(t.Context(), testAccount, "unmetered_by_catalog")

		test.ErrorIs(t, err, metering.ErrNoQuota)
	})

	T.Run("an unregistered meter is an unknown meter", func(t *testing.T) {
		t.Parallel()

		_, err := newQuotaSource(t, staticPlans("pro")).QuotaFor(t.Context(), testAccount, "nope")

		test.ErrorIs(t, err, metering.ErrUnknownMeter)
	})

	T.Run("a plan source failure is reported", func(t *testing.T) {
		t.Parallel()

		// No fallback here, deliberately: this is consulted from metering's exact
		// path, and an exact answer has nowhere to degrade to.
		errPlans := platformerrors.New("plan store is down")

		_, err := newQuotaSource(t, failingPlans(errPlans)).QuotaFor(t.Context(), testAccount, testMeter)

		test.ErrorIs(t, err, errPlans)
	})

	T.Run("an account with no plan has no quota", func(t *testing.T) {
		t.Parallel()

		_, err := newQuotaSource(t, failingPlans(ErrNoPlan)).QuotaFor(t.Context(), testAccount, testMeter)

		test.ErrorIs(t, err, ErrNoPlan)
	})

	T.Run("the quota it serves is the limit the checker reports", func(t *testing.T) {
		t.Parallel()

		// The whole point of the arrangement: one number, so the limit an account
		// is shown is the limit enforced against it.
		catalog := newCatalog(t)
		grant, ok := catalog.GrantFor("pro", "llm_tokens")
		must.True(t, ok)

		q, err := newQuotaSource(t, staticPlans("pro")).QuotaFor(t.Context(), testAccount, testMeter)

		must.NoError(t, err)
		test.EqOp(t, grant.Limit, q.Limit)
	})
}
