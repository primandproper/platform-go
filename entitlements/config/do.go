package entitlementscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/entitlements"
	"github.com/primandproper/platform-go/v13/featureflags"
	"github.com/primandproper/platform-go/v13/internal/injection"
	"github.com/primandproper/platform-go/v13/metering"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterCatalog registers an *entitlements.Catalog with the injector.
//
// Prerequisites: *Config and []entitlements.Feature (the application's feature
// declarations) must be registered before the Catalog is invoked. The features
// are a registered value rather than something this package can derive — see
// the package documentation on why they are code and plans are configuration.
func RegisterCatalog(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*entitlements.Catalog, error) {
		return NewCatalog(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[[]entitlements.Feature](i),
		)
	})
}

// RegisterQuotaSource registers an *entitlements.QuotaSource with the injector.
//
// Register it for any deployment whose catalog has quota features, and give it
// to the metering enforcer — meteringcfg.RegisterEnforcer resolves a
// metering.QuotaSource from the injector, so registering this one as that
// interface is what makes the catalog's limits the enforced limits:
//
//	do.Provide(i, func(i do.Injector) (metering.QuotaSource, error) {
//	    return do.Invoke[*entitlements.QuotaSource](i)
//	})
//
// It is not registered as metering.QuotaSource here. Doing that would have
// importing this package silently change what a metering enforcer elsewhere in
// the same container enforces, and a container-wide behavior change is not
// something a registration function should make on a caller's behalf.
//
// Prerequisites: *Config, *entitlements.Catalog (see RegisterCatalog),
// entitlements.PlanSource, and *metering.Registry must be registered before the
// QuotaSource is invoked.
func RegisterQuotaSource(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*entitlements.QuotaSource, error) {
		return NewQuotaSource(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[*entitlements.Catalog](i),
			do.MustInvoke[entitlements.PlanSource](i),
			do.MustInvoke[*metering.Registry](i),
		)
	})
}

// RegisterChecker registers an entitlements.Checker with the injector.
//
// The enforcer, the flag manager, and the assignment cache are all optional. An
// absent enforcer is fine for a catalog with no quota features and an error for
// one with them; an absent flag manager makes every grant and kill flag inert;
// an absent cache resolves the account's plan on every check.
//
// Prerequisites: *Config, *entitlements.Catalog (see RegisterCatalog), and
// entitlements.PlanSource must be registered before the Checker is invoked.
func RegisterChecker(i do.Injector) {
	do.Provide(i, func(i do.Injector) (entitlements.Checker, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		enforcer, err := injection.InvokeOptional[metering.Enforcer](i)
		if err != nil {
			return nil, err
		}

		flags, err := injection.InvokeOptional[featureflags.FeatureFlagManager](i)
		if err != nil {
			return nil, err
		}

		assignments, err := injection.InvokeOptional[cache.Cache[entitlements.Assignment]](i)
		if err != nil {
			return nil, err
		}

		// Built into a variable and returned only once err is known to be nil:
		// NewChecker returns an *entitlements.PlanChecker, and returning it
		// straight through would register a non-nil entitlements.Checker
		// wrapping a nil pointer whenever construction failed — which is
		// exactly the "nobody registered one" / "the registered one failed to
		// build" distinction the InvokePillars call above exists to keep.
		checker, err := NewChecker(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[*entitlements.Catalog](i),
			do.MustInvoke[entitlements.PlanSource](i),
			enforcer,
			flags,
			assignments,
			WithPillars(pillars),
		)
		if err != nil {
			return nil, err
		}

		return checker, nil
	})
}
