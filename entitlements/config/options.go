package entitlementscfg

import (
	"github.com/primandproper/platform-go/v14/entitlements"
	"github.com/primandproper/platform-go/v14/metering"

	"github.com/primandproper/primitives-go/v2/cache"
	"github.com/primandproper/primitives-go/v2/featureflags"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures how this package's constructors assemble what they build.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. Requiring them positionally would make a caller that wants none of
// the three name all three anyway, usually as noops.
//
// The enforcer, the flag manager, and the assignment cache are options for the
// same reason and were parameters until they were not: each is a dependency a
// legitimate deployment simply has none of, and a positional parameter made
// every one of those deployments spell a nil the constructor's own
// documentation had already predicted.
//
// NewCatalog and NewQuotaSource accept the type and ignore every value of it.
// Neither builds anything that observes, and neither takes a dependency any of
// the options name: a catalog is a map assembled at startup, and a quota source
// is a lookup metering traces from its own side. They take the parameter so that
// one wiring site can pass the same options to all three constructors without
// knowing which of them care.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	enforcer    metering.Enforcer
	flags       featureflags.FeatureFlagManager
	assignments cache.Cache[entitlements.Assignment]

	checker []entitlements.CheckerOption
}

// newOptions applies opts, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on the
// instrumented operations. An absent tracer provider traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithPillars attaches a logger, tracer provider, and metrics provider in one
// go, for the common case where a caller has already built them together. A nil
// Pillars attaches nothing.
//
// It is applied in order with the individual options, so a caller can hand over
// its pillars and then override one of them.
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, o.metricsProvider = p.Deps() }
}

// WithEnforcer attaches the metering enforcer NewChecker consults for quota
// features. It is required when the catalog has any quota feature — see
// entitlements.ErrEnforcerRequired — and pointless when it does not: a
// deployment gating only boolean features needs no metering tables and no store.
// Other constructors ignore it.
func WithEnforcer(enforcer metering.Enforcer) Option {
	return func(o *options) { o.enforcer = enforcer }
}

// WithFeatureFlags attaches the flag manager NewChecker consults for per-account
// grants and kills. Absent, every grant and kill flag is inert and decisions come
// from the plan alone. Other constructors ignore it.
func WithFeatureFlags(flags featureflags.FeatureFlagManager) Option {
	return func(o *options) { o.flags = flags }
}

// WithAssignmentCache attaches the cache NewChecker resolves plan assignments
// through. Absent, the account's plan is resolved from the PlanSource on every
// check. Other constructors ignore it.
func WithAssignmentCache(assignments cache.Cache[entitlements.Assignment]) Option {
	return func(o *options) { o.assignments = assignments }
}

// WithCheckerOptions passes opts to NewChecker, which applies them after the
// options it derives from configuration — so a caller can override anything. The
// other constructors ignore them.
func WithCheckerOptions(opts ...entitlements.CheckerOption) Option {
	return func(o *options) { o.checker = append(o.checker, opts...) }
}
