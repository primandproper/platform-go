package entitlements_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v10/authorization"
	"github.com/primandproper/platform-go/v10/entitlements"
	"github.com/primandproper/platform-go/v10/metering"
)

// buildCatalog declares the features the code gates on and the plans that
// include them.
func buildCatalog() *entitlements.Catalog {
	catalog := entitlements.NewCatalog()

	_ = catalog.RegisterFeature(entitlements.Feature{
		Key:       "advanced_search",
		Kind:      entitlements.KindBoolean,
		GrantFlag: "advanced-search-rollout",
	})
	_ = catalog.RegisterFeature(entitlements.Feature{
		Key:   "llm_tokens",
		Kind:  entitlements.KindQuota,
		Meter: "llm_tokens",
	})

	_ = catalog.RegisterPlan(entitlements.Plan{Name: "free"})
	_ = catalog.RegisterPlan(entitlements.Plan{
		Name: "pro",
		Includes: []entitlements.Grant{
			{Feature: "advanced_search"},
			{Feature: "llm_tokens", Limit: 5_000_000, Behavior: metering.BehaviorAllowOverage},
		},
	})

	return catalog
}

func ExampleChecker() {
	// A boolean-only wiring: no metering enforcer, no store, no migrations.
	catalog := entitlements.NewCatalog()
	_ = catalog.RegisterFeature(entitlements.Feature{Key: "sso", Kind: entitlements.KindBoolean})
	_ = catalog.RegisterPlan(entitlements.Plan{Name: "free"})
	_ = catalog.RegisterPlan(entitlements.Plan{
		Name:     "pro",
		Includes: []entitlements.Grant{{Feature: "sso"}},
	})

	checker, err := entitlements.NewPlanChecker(
		context.Background(),
		&entitlements.CheckerConfig{},
		catalog,
		entitlements.NewStaticPlanSource("pro"),
	)
	if err != nil {
		panic(err)
	}

	decision, err := checker.Check(context.Background(), "account_123", "sso")
	if err != nil {
		panic(err)
	}

	fmt.Println(decision.Allowed, decision.Reason)
	// Output: true plan_includes
}

func ExampleChecker_denial() {
	checker, err := entitlements.NewPlanChecker(
		context.Background(),
		&entitlements.CheckerConfig{},
		buildCatalog(),
		entitlements.NewStaticPlanSource("free"),
		// A quota feature in the catalog needs an enforcer, even for a check that
		// never reaches one.
		entitlements.WithEnforcer(noopEnforcer{}),
	)
	if err != nil {
		panic(err)
	}

	decision, err := checker.Check(context.Background(), "account_123", "advanced_search")
	if err != nil {
		panic(err)
	}

	// A handler returns this and gets a 402 with a code saying which denial it
	// was; errors/http does the mapping.
	fmt.Println(decision.Allowed, decision.Reason, decision.Err())
	// Output: false plan_excludes not entitled
}

func ExampleChecker_permissions() {
	checker, err := entitlements.NewPlanChecker(
		context.Background(),
		&entitlements.CheckerConfig{},
		buildCatalog(),
		entitlements.NewStaticPlanSource("pro"),
		entitlements.WithEnforcer(noopEnforcer{}),
	)
	if err != nil {
		panic(err)
	}

	entitled, err := checker.Permissions(context.Background(), "account_123")
	if err != nil {
		panic(err)
	}

	// OR'd with whatever the principal's roles grant, exactly as authorization
	// merges service-wide and per-tenant authority.
	grants := authorization.NewGrants(authorization.NewPermissionSet("update.recipes"), entitled)

	fmt.Println(grants.Has("update.recipes"), grants.Has("entitlement.advanced_search"))
	// Quota features are absent: their answer changes between two checks in the
	// same request, and a permission set is read without asking again.
	fmt.Println(grants.Has("entitlement.llm_tokens"))
	// Output:
	// true true
	// false
}

// noopEnforcer stands in for a real metering.Enforcer in examples that never
// reach a quota feature. A real wiring builds one over a store — see the
// metering package — and gives it entitlements.NewQuotaSource.
type noopEnforcer struct{}

func (noopEnforcer) Check(context.Context, string, string, int64) (*metering.Decision, error) {
	return &metering.Decision{}, nil
}

func (noopEnforcer) Consume(context.Context, string, string, int64) (*metering.Decision, error) {
	return &metering.Decision{}, nil
}

func (noopEnforcer) ConsumeUsage(context.Context, metering.Usage) (*metering.Decision, error) {
	return &metering.Decision{}, nil
}
