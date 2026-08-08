package entitlements

import (
	"github.com/primandproper/platform-go/v10/cache"
	"github.com/primandproper/platform-go/v10/featureflags"
	"github.com/primandproper/platform-go/v10/metering"
	"github.com/primandproper/platform-go/v10/observability/logging"
	"github.com/primandproper/platform-go/v10/observability/metrics"
	"github.com/primandproper/platform-go/v10/observability/tracing"
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
