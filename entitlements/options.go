package entitlements

import (
	"maps"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/featureflags"
	"github.com/primandproper/platform-go/v13/metering"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// CheckerOption configures a PlanChecker.
type CheckerOption func(*PlanChecker)

// WithEnforcer attaches the metering enforcer quota features are answered from.
//
// It is required for a Catalog with any quota feature — see ErrEnforcerRequired
// — and pointless for one with none, which is why it is an option rather than a
// parameter. A deployment gating only boolean features needs no metering tables,
// no migrations, and no store.
//
// It should be an enforcer wired with this package's QuotaSource; see
// NewQuotaSource for what that buys and what happens without it.
func WithEnforcer(enforcer metering.Enforcer) CheckerOption {
	return func(c *PlanChecker) {
		if enforcer != nil {
			c.enforcer = enforcer
		}
	}
}

// WithFeatureFlags attaches the flag manager grant and kill flags are evaluated
// against.
//
// Without one, every flag is inert and decisions come from the plan alone. That
// is a real configuration rather than a degraded one — a deployment with no
// rollouts and no overrides — and it is what makes the package usable before a
// flag provider exists.
func WithFeatureFlags(flags featureflags.FeatureFlagManager) CheckerOption {
	return func(c *PlanChecker) {
		if flags != nil {
			c.flags = flags
		}
	}
}

// WithCache attaches the cache account-to-plan resolutions are read through.
//
// Without one, every Check calls the PlanSource. That is correct and it is
// usually the wrong trade: the plan is a row that changes a few times a year
// being read on every request that gates on anything.
//
// Only the plan assignment is cached here. Flag evaluations are not — every
// provider in this module evaluates locally against rules it already holds, so
// caching them would add staleness to something that is already fast, and would
// freeze a percentage rollout's answer for an account that the provider means to
// re-evaluate. Usage totals are not either: metering caches its own, with a
// staleness budget set per meter by whoever knows what that meter is worth.
func WithCache(c cache.Cache[Assignment]) CheckerOption {
	return func(pc *PlanChecker) {
		if c != nil {
			pc.cache = c
		}
	}
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) CheckerOption {
	return func(c *PlanChecker) {
		c.logger = logger
	}
}

// WithTracerProvider attaches a tracer provider.
func WithTracerProvider(tracerProvider tracing.Provider) CheckerOption {
	return func(c *PlanChecker) {
		c.tracerProvider = tracerProvider
	}
}

// WithMetricsProvider attaches a metrics provider.
//
// The counters worth alerting on are entitlements_plan_faults, which is a plan
// store that has stopped answering, and entitlements_limit_mismatches, which is
// a catalog whose limits are not the ones being enforced — see NewQuotaSource.
func WithMetricsProvider(metricsProvider metrics.Provider) CheckerOption {
	return func(c *PlanChecker) {
		c.metricsProvider = metricsProvider
	}
}

// CheckOption configures a single call to Check, CheckQuantity, or Permissions.
//
// It is a separate type from CheckerOption because the two are answered at
// different times: a CheckerOption is applied once, when the checker is built,
// and a CheckOption describes the request in hand.
type CheckOption func(*checkOptions)

// checkOptions is the resolved per-call configuration.
type checkOptions struct {
	attributes map[string]any
}

func newCheckOptions(opts []CheckOption) *checkOptions {
	co := &checkOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(co)
		}
	}

	return co
}

// WithTargetingAttributes supplies additional signals for this call's flag
// evaluations, carried to the provider as featureflags.EvaluationContext
// Attributes alongside the account, which is always the targeting key.
//
// The account alone answers "is this account in the rollout". It does not answer
// the questions a rollout is usually actually written against — region, plan
// tier, beta cohort, the particular workspace inside the account — and a flag
// provider can only target on what it was given. Without this, a rule that
// depends on any of them evaluates against a context that omits it and silently
// takes the default branch.
//
//	dec, err := checker.Check(ctx, account, "advanced_search",
//		entitlements.WithTargetingAttributes(map[string]any{
//			"region":    req.Region,
//			"workspace": req.WorkspaceID,
//		}))
//
// It reaches the flag provider and nothing else: plan resolution, the plan
// cache, and quota accounting are all keyed on the account, and an attribute
// that changed one of them would make two calls for the same account disagree
// about what that account is entitled to.
//
// The map is not recorded on spans or logs. These are caller-chosen keys and
// values, and an exporter is not the place to discover that somebody started
// targeting on an email address.
//
// Note the value type is any rather than string: it is passed through to
// featureflags.EvaluationContext unchanged, and providers there target on
// booleans and numbers as well as strings.
func WithTargetingAttributes(attributes map[string]any) CheckOption {
	return func(co *checkOptions) {
		if len(attributes) == 0 {
			return
		}

		if co.attributes == nil {
			co.attributes = make(map[string]any, len(attributes))
		}

		maps.Copy(co.attributes, attributes)
	}
}
