/*
Package entitlementscfg assembles the entitlements machinery from configuration:
the Catalog plans are read into, the Checker that answers questions against it,
and the metering.QuotaSource that keeps metering enforcing the catalog's limits
rather than its own.

The split between what is here and what is not is the same one the entitlements
package documents. Plans are configuration — which tier includes what, and how
much of it, changes when pricing changes, which is more often than a deploy and
by people who do not ship one — so they are read from a file or an environment
overlay through the root config package's loaders. Features are not, and are
passed to NewCatalog as Go values: a quota feature names a meter the application
registered, and a boolean one names a permission its handlers check, and neither
means anything the code has not already been written for.

That is also why the plan list is json and yaml rather than env. A catalog of
tiers with limits per feature is a document, and expressing one in flat
environment variables produces something nobody can review.
*/
package entitlementscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/entitlements"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/featureflags"
	"github.com/primandproper/platform-go/v13/metering"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles an entitlements Catalog and Checker.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// Checker carries the read path's knobs.
	Checker entitlements.CheckerConfig `env:",init" envPrefix:"CHECKER_" json:"checker,omitzero" yaml:"checker,omitempty"`

	// Plans are the tiers and what each one includes.
	//
	// They have no env tag. A plan is a name and a list of grants with limits,
	// and the flat key-value shape of the environment can express that only as
	// something encoded — which is a config file with extra steps, and one no
	// reviewer can read in a diff. Load them from JSON, TOML, or YAML through the
	// config package's file loaders, which overlay the environment on top for the
	// scalars that do have env tags.
	Plans []entitlements.Plan `json:"plans,omitempty" yaml:"plans,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	cfg.Checker.EnsureDefaults()
}

// ValidateWithContext validates a Config.
//
// The nested config is validated through a validation.By closure because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// it would otherwise be skipped.
//
// The plans are not validated here. Whether a grant names a real feature, and
// whether its limit makes sense for that feature's kind, are questions only a
// Catalog holding the features can answer — so NewCatalog asks them, and reports
// the offending plan and feature by name.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Checker, validation.By(func(any) error {
			return cfg.Checker.ValidateWithContext(ctx)
		})),
	)
}

// prepare fills defaults and validates, which every constructor below does first
// and identically.
func (cfg *Config) prepare(ctx context.Context) error {
	if cfg == nil {
		return errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return errors.Wrap(err, "validating entitlements config")
	}

	return nil
}

// NewCatalog builds the Catalog from code-declared features and configured
// plans.
//
// The order is the one Catalog requires and the one that produces the useful
// error: features first, then the plans that grant them, so a plan naming a
// feature that does not exist is reported as the typo it is rather than
// registering an entitlement nothing asks for.
//
// features is a required argument rather than a config field for the reason this
// package's documentation gives: a feature is a fact about the code that reads
// it.
func NewCatalog(
	ctx context.Context,
	cfg *Config,
	features []entitlements.Feature,
	_ ...Option,
) (*entitlements.Catalog, error) {
	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	catalog := entitlements.NewCatalog()

	for i := range features {
		if err := catalog.RegisterFeature(features[i]); err != nil {
			return nil, errors.Wrap(err, "registering entitlement feature")
		}
	}

	for i := range cfg.Plans {
		if err := catalog.RegisterPlan(cfg.Plans[i]); err != nil {
			return nil, errors.Wrap(err, "registering entitlement plan")
		}
	}

	return catalog, nil
}

// NewChecker builds the read path.
//
// enforcer is required when the catalog has any quota feature and pointless when
// it does not — a deployment gating only boolean features needs no metering
// tables and no store. flags may be nil, in which case every grant and kill flag
// is inert and decisions come from the plan alone. assignments may be nil, at the
// cost of resolving the account's plan on every check.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything.
func NewChecker(
	ctx context.Context,
	cfg *Config,
	catalog *entitlements.Catalog,
	plans entitlements.PlanSource,
	enforcer metering.Enforcer,
	flags featureflags.FeatureFlagManager,
	assignments cache.Cache[entitlements.Assignment],
	opts ...Option,
) (*entitlements.PlanChecker, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	var base []entitlements.CheckerOption
	if enforcer != nil {
		base = append(base, entitlements.WithEnforcer(enforcer))
	}
	if flags != nil {
		base = append(base, entitlements.WithFeatureFlags(flags))
	}
	if assignments != nil {
		base = append(base, entitlements.WithCache(assignments))
	}
	if logger != nil {
		base = append(base, entitlements.WithLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, entitlements.WithTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, entitlements.WithMetricsProvider(metricsProvider))
	}

	return entitlements.NewPlanChecker(ctx, &cfg.Checker, catalog, plans, append(base, o.checker...)...)
}

// NewQuotaSource builds the adapter that serves the catalog's plan limits to
// metering.
//
// It must be given to the enforcer NewChecker is then given, so that the limit
// an account is shown and the limit enforced against it are the same number —
// see entitlements.NewQuotaSource for what happens when they are not. That
// ordering is the one thing about this wiring that cannot be expressed in
// configuration, which is why the DI registrations in do.go do it once.
func NewQuotaSource(
	ctx context.Context,
	cfg *Config,
	catalog *entitlements.Catalog,
	plans entitlements.PlanSource,
	registry *metering.Registry,
	_ ...Option,
) (*entitlements.QuotaSource, error) {
	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	return entitlements.NewQuotaSource(catalog, plans, registry)
}
