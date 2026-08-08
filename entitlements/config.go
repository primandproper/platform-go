package entitlements

import (
	"context"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultCacheTTL is how long an account's resolved plan is cached.
	//
	// Thirty seconds. The trade is not symmetric, which is why it is short. Long,
	// and a customer who has just paid keeps being told they have not — the
	// worst half-minute in a signup funnel, and the one support ticket this
	// package exists to prevent. Short, and the plan lookup runs more often,
	// which is one indexed read of a row that is already hot.
	//
	// It bounds the wrong answer for a downgrade too, and that direction matters
	// less: continuing to serve a feature for thirty seconds after a plan lapses
	// costs a fraction of a request's worth of it. Callers who cannot accept even
	// that call Checker.Invalidate from the webhook that processed the change,
	// which closes the window in the process that handled it.
	DefaultCacheTTL = 30 * time.Second

	// DefaultCachePrefix namespaces the checker's cache keys, so a plan
	// assignment cannot collide with an unrelated entry in a cache shared with
	// something else.
	DefaultCachePrefix = "entitlements:"

	// MaxCacheTTL bounds CacheTTL.
	//
	// Ten minutes. Past that the cache is not accelerating the plan lookup, it is
	// deciding entitlements — an account whose subscription changed is served the
	// old answer for longer than anybody watching a deploy would wait, and the
	// staleness stops being a latency trade and becomes a correctness one. An
	// operator who wants the plan lookup to happen less often than this wants a
	// different PlanSource, not a longer TTL.
	MaxCacheTTL = 10 * time.Minute
)

// CheckerConfig configures a PlanChecker.
type CheckerConfig struct {
	// CachePrefix namespaces cache keys. Defaults to DefaultCachePrefix.
	CachePrefix string `env:"CACHE_PREFIX" json:"cachePrefix,omitempty" yaml:"cachePrefix,omitempty"`

	// FallbackPlan is the plan used when the PlanSource fails.
	//
	// Empty — the default — denies: an entitlement that cannot be resolved is not
	// granted. That is the right answer whenever the features being gated are the
	// ones being sold, because an outage that hands every account the top tier is
	// an outage that costs the operator money for as long as nobody notices.
	//
	// Naming a plan here is the other answer, and it is better than the boolean
	// "fail open" it replaces: the degraded state is a plan somebody chose and
	// can read in the catalog, rather than "everything". Naming the free tier
	// means a plan-store outage degrades paying customers to free rather than
	// locking them out, which for a product whose core is on the free tier is the
	// difference between a slow morning and an incident.
	//
	// It applies only to a failed resolution. An account the PlanSource
	// authoritatively says has no plan — ErrNoPlan — is denied whatever this
	// says; that is not an outage, that is a customer who has not paid.
	//
	// It also applies only to boolean features, and the asymmetry is worth
	// knowing before relying on it. A quota feature's limit is served to metering
	// through QuotaSource, which the enforcer consults on both its cached path
	// and its exact one, and which has no fallback: a Check of a quota feature
	// during a plan-store outage therefore returns an error rather than a
	// degraded allowance. That is deliberate. The same source answers
	// metering.Enforcer.Consume, which records usage in the same transaction it
	// decides in, and an exact path that enforced a guessed limit would write
	// consumption against a plan the customer is not on — a number somebody has
	// to unpick from an invoice later. Refusing to guess costs an error during an
	// outage; guessing costs a reconciliation after one.
	//
	// What narrows that window is the cache: an outage only reaches accounts
	// whose assignment has expired.
	FallbackPlan string `env:"FALLBACK_PLAN" json:"fallbackPlan,omitempty" yaml:"fallbackPlan,omitempty"`

	// CacheTTL is how long an account's resolved plan is cached. Defaults to
	// DefaultCacheTTL and may not exceed MaxCacheTTL.
	CacheTTL time.Duration `env:"CACHE_TTL" json:"cacheTTL,omitempty" yaml:"cacheTTL,omitempty"`
}

var _ validation.ValidatableWithContext = (*CheckerConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *CheckerConfig) EnsureDefaults() {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}

	if cfg.CachePrefix == "" {
		cfg.CachePrefix = DefaultCachePrefix
	}
}

// ValidateWithContext validates a CheckerConfig.
//
// FallbackPlan is not validated here — whether it names a real plan is a
// question only the Catalog can answer, and NewPlanChecker asks it there. A
// fallback naming a plan nobody registered would deny everything during exactly
// the outage it was configured to survive.
func (cfg *CheckerConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.CacheTTL, validation.Required, validation.Max(MaxCacheTTL)),
	)
}
