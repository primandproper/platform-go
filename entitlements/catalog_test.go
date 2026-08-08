package entitlements

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v10/authorization"
	"github.com/primandproper/platform-go/v10/metering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// booleanFeature is the feature every boolean-path test registers.
func booleanFeature() Feature {
	return Feature{Key: "advanced_search", Kind: KindBoolean}
}

// quotaFeature is the feature every quota-path test registers.
func quotaFeature() Feature {
	return Feature{Key: "llm_tokens", Kind: KindQuota, Meter: "llm_tokens"}
}

func TestCatalog_RegisterFeature(T *testing.T) {
	T.Parallel()

	T.Run("registers a boolean feature and defaults its permission", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(booleanFeature()))

		f, ok := c.Feature("advanced_search")
		must.True(t, ok)
		test.EqOp(t, authorization.Permission("entitlement.advanced_search"), f.Permission)
	})

	T.Run("keeps an explicit permission", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(Feature{
			Key:        "advanced_search",
			Kind:       KindBoolean,
			Permission: "search.advanced",
		}))

		f, _ := c.Feature("advanced_search")
		test.EqOp(t, authorization.Permission("search.advanced"), f.Permission)
	})

	T.Run("rejects a duplicate key", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(booleanFeature()))

		test.ErrorIs(t, c.RegisterFeature(booleanFeature()), ErrDuplicateFeature)
	})

	T.Run("rejects a key that is not a plain identifier", func(t *testing.T) {
		t.Parallel()

		for _, key := range []string{"", "has space", "has-dash", "1leading", "has:colon", "has\x00nul"} {
			c := NewCatalog()
			err := c.RegisterFeature(Feature{Key: key, Kind: KindBoolean})

			test.ErrorIs(t, err, ErrInvalidFeatureKey, test.Sprintf("key %q", key))
		}
	})

	T.Run("rejects an unnamed kind", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()

		test.ErrorIs(t, c.RegisterFeature(Feature{Key: "thing"}), ErrInvalidKind)
	})

	T.Run("rejects a quota feature with no meter", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()

		test.ErrorIs(t, c.RegisterFeature(Feature{Key: "tokens", Kind: KindQuota}), ErrMeterRequired)
	})

	T.Run("rejects a boolean feature with a meter", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		err := c.RegisterFeature(Feature{Key: "sso", Kind: KindBoolean, Meter: "sso"})

		test.ErrorIs(t, err, ErrMeterNotAllowed)
	})

	T.Run("rejects a grant flag on a quota feature", func(t *testing.T) {
		t.Parallel()

		// The combination has nowhere for a limit to come from; see
		// ErrGrantFlagNotAllowed.
		c := NewCatalog()
		f := quotaFeature()
		f.GrantFlag = "tokens-rollout"

		test.ErrorIs(t, c.RegisterFeature(f), ErrGrantFlagNotAllowed)
	})

	T.Run("permits a kill flag on a quota feature", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		f := quotaFeature()
		f.KillFlag = "tokens-kill"

		test.NoError(t, c.RegisterFeature(f))
	})
}

func TestCatalog_RegisterPlan(T *testing.T) {
	T.Parallel()

	T.Run("registers a plan over known features", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(booleanFeature()))
		must.NoError(t, c.RegisterFeature(quotaFeature()))

		must.NoError(t, c.RegisterPlan(Plan{
			Name: "pro",
			Includes: []Grant{
				{Feature: "advanced_search"},
				{Feature: "llm_tokens", Limit: 100},
			},
		}))

		g, ok := c.GrantFor("pro", "llm_tokens")
		must.True(t, ok)
		test.EqOp(t, int64(100), g.Limit)
	})

	T.Run("defaults a quota grant's behavior to block", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(quotaFeature()))
		must.NoError(t, c.RegisterPlan(Plan{Name: "free", Includes: []Grant{{Feature: "llm_tokens", Limit: 10}}}))

		g, _ := c.GrantFor("free", "llm_tokens")
		test.EqOp(t, metering.BehaviorBlock, g.Behavior)
	})

	T.Run("keeps an explicit behavior", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(quotaFeature()))
		must.NoError(t, c.RegisterPlan(Plan{Name: "pro", Includes: []Grant{
			{Feature: "llm_tokens", Limit: 10, Behavior: metering.BehaviorAllowOverage},
		}}))

		g, _ := c.GrantFor("pro", "llm_tokens")
		test.EqOp(t, metering.BehaviorAllowOverage, g.Behavior)
	})

	T.Run("rejects an invalid plan name", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()

		test.ErrorIs(t, c.RegisterPlan(Plan{Name: "not a plan"}), ErrInvalidPlanName)
	})

	T.Run("rejects a duplicate plan", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterPlan(Plan{Name: "pro"}))

		test.ErrorIs(t, c.RegisterPlan(Plan{Name: "pro"}), ErrDuplicatePlan)
	})

	T.Run("rejects a grant naming an unregistered feature", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		err := c.RegisterPlan(Plan{Name: "pro", Includes: []Grant{{Feature: "nope"}}})

		test.ErrorIs(t, err, ErrUnknownFeature)
	})

	T.Run("rejects one plan granting a feature twice", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(booleanFeature()))

		err := c.RegisterPlan(Plan{Name: "pro", Includes: []Grant{
			{Feature: "advanced_search"},
			{Feature: "advanced_search"},
		}})

		test.ErrorIs(t, err, ErrDuplicateGrant)
	})

	T.Run("rejects a quantity on a boolean grant", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(booleanFeature()))

		err := c.RegisterPlan(Plan{Name: "pro", Includes: []Grant{{Feature: "advanced_search", Limit: 5}}})

		test.ErrorIs(t, err, ErrLimitOnBooleanFeature)
	})

	T.Run("rejects a negative limit", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(quotaFeature()))

		err := c.RegisterPlan(Plan{Name: "pro", Includes: []Grant{{Feature: "llm_tokens", Limit: -1}}})

		test.ErrorIs(t, err, ErrNegativeLimit)
	})

	T.Run("a failed registration leaves the plan unregistered", func(t *testing.T) {
		t.Parallel()

		// The grants map is built before either map is written, so a plan whose
		// second grant is bad does not leave the first one reachable.
		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(booleanFeature()))

		must.Error(t, c.RegisterPlan(Plan{Name: "pro", Includes: []Grant{
			{Feature: "advanced_search"},
			{Feature: "nope"},
		}}))

		_, ok := c.Plan("pro")
		test.False(t, ok)

		_, granted := c.GrantFor("pro", "advanced_search")
		test.False(t, granted)
	})
}

func TestCatalog_lookups(T *testing.T) {
	T.Parallel()

	T.Run("reports sorted keys and names", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(quotaFeature()))
		must.NoError(t, c.RegisterFeature(booleanFeature()))
		must.NoError(t, c.RegisterPlan(Plan{Name: "pro"}))
		must.NoError(t, c.RegisterPlan(Plan{Name: "free"}))

		test.Eq(t, []string{"advanced_search", "llm_tokens"}, c.FeatureKeys())
		test.Eq(t, []string{"free", "pro"}, c.PlanNames())
		test.SliceLen(t, 2, c.Features())
		test.SliceLen(t, 2, c.Plans())
	})

	T.Run("an unregistered plan grants nothing", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(booleanFeature()))

		_, ok := c.GrantFor("nonexistent", "advanced_search")
		test.False(t, ok)
	})

	T.Run("reports whether any feature is quota bound", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		test.False(t, c.HasQuotaFeatures())

		must.NoError(t, c.RegisterFeature(booleanFeature()))
		test.False(t, c.HasQuotaFeatures())

		must.NoError(t, c.RegisterFeature(quotaFeature()))
		test.True(t, c.HasQuotaFeatures())
	})
}

func TestCatalog_ValidateMeters(T *testing.T) {
	T.Parallel()

	T.Run("accepts a catalog whose meters are all registered", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(quotaFeature()))
		must.NoError(t, c.RegisterFeature(booleanFeature()))

		test.NoError(t, c.ValidateMeters(newRegistry(t)))
	})

	T.Run("rejects a quota feature naming an unregistered meter", func(t *testing.T) {
		t.Parallel()

		c := NewCatalog()
		must.NoError(t, c.RegisterFeature(Feature{Key: "seats", Kind: KindQuota, Meter: "seats"}))

		err := c.ValidateMeters(newRegistry(t))

		test.ErrorIs(t, err, metering.ErrUnknownMeter)
	})

	T.Run("rejects a nil registry", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, NewCatalog().ValidateMeters(nil), ErrNilRegistry)
	})
}

func TestDecision_Err(T *testing.T) {
	T.Parallel()

	T.Run("a nil decision fails closed", func(t *testing.T) {
		t.Parallel()

		var d *Decision

		test.ErrorIs(t, d.Err(), ErrNotEntitled)
	})

	T.Run("an allowed decision is no error", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, (&Decision{Allowed: true}).Err())
	})

	T.Run("an exhausted quota is its own sentinel", func(t *testing.T) {
		t.Parallel()

		err := (&Decision{Reason: ReasonQuotaExhausted}).Err()

		test.ErrorIs(t, err, ErrQuotaExhausted)
		test.False(t, errors.Is(err, ErrNotEntitled))
	})

	T.Run("every other denial is not-entitled", func(t *testing.T) {
		t.Parallel()

		for _, reason := range []Reason{
			ReasonPlanExcludes, ReasonFlagKilled, ReasonNoPlan, ReasonUnknownPlan, ReasonPlanUnavailable,
		} {
			err := (&Decision{Reason: reason}).Err()

			test.ErrorIs(t, err, ErrNotEntitled, test.Sprintf("reason %q", reason))
		}
	})
}

func TestKind_Valid(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.True(t, KindBoolean.Valid())
		test.True(t, KindQuota.Valid())
		test.False(t, Kind("").Valid())
		test.False(t, Kind("counter").Valid())
	})
}
