package meteringcfg

import (
	"github.com/primandproper/platform-go/v14/metering"

	"github.com/primandproper/primitives-go/analytics"
	"github.com/primandproper/primitives-go/cache"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
)

// Option configures how this package's constructors assemble what they build.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. Requiring them positionally made a caller that wanted none of the
// three name all three anyway, usually as noops.
//
// The analytics reporter, the quota source and the totals cache are options for
// the same reason and were parameters until they were not: each is a dependency
// a legitimate deployment simply has none of, and a positional parameter made
// every one of those deployments spell a nil the constructor's own
// documentation had already predicted.
//
// The dependency options and the passthrough options each apply to one
// constructor and are ignored by the others, so a single wiring site can carry
// options for whichever component it happens to build. They cannot be a second
// variadic on the constructor: Go allows one per function, and that slot is what
// makes the observability optional.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	analytics analytics.EventReporter
	quotas    metering.QuotaSource
	totals    cache.Cache[metering.CachedTotal]

	store    []metering.SQLStoreOption
	recorder []metering.RecorderOption
	enforcer []metering.EnforcerOption
	flusher  []metering.FlusherOption
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

// WithRecorderAnalytics attaches the reporter NewRecorder mirrors usage events
// to. Absent — which is usually what a deployment wants — nothing is mirrored;
// see metering.WithRecorderAnalytics for why it is off by default. The other
// constructors ignore it.
//
// It is not NewFlusher's reporter, which is a capitalism.UsageReporter, is
// required, and stays a parameter for the reason NewFlusher's documentation
// gives.
func WithRecorderAnalytics(reporter analytics.EventReporter) Option {
	return func(o *options) { o.analytics = reporter }
}

// WithEnforcerQuotaSource attaches the source NewEnforcer reads per-subject
// limits from — entitlementscfg.NewQuotaSource builds the one that keeps the
// limit an account is shown and the limit enforced against it the same number.
// Absent, the Registry's static quotas serve every subject. The other
// constructors ignore it.
func WithEnforcerQuotaSource(quotas metering.QuotaSource) Option {
	return func(o *options) { o.quotas = quotas }
}

// WithEnforcerCache attaches the totals cache NewEnforcer answers Check from.
// Absent, every Check is a durable read — see metering.WithEnforcerCache. The
// other constructors ignore it.
func WithEnforcerCache(totals cache.Cache[metering.CachedTotal]) Option {
	return func(o *options) { o.totals = totals }
}

// WithStoreOptions passes opts to NewStore, which applies them after the options it
// derives from configuration — so a caller can override anything. The other
// constructors ignore them.
func WithStoreOptions(opts ...metering.SQLStoreOption) Option {
	return func(o *options) { o.store = append(o.store, opts...) }
}

// WithRecorderOptions passes opts to NewRecorder, which applies them after the options it
// derives from configuration — so a caller can override anything. The other
// constructors ignore them.
func WithRecorderOptions(opts ...metering.RecorderOption) Option {
	return func(o *options) { o.recorder = append(o.recorder, opts...) }
}

// WithEnforcerOptions passes opts to NewEnforcer, which applies them after the options it
// derives from configuration — so a caller can override anything. The other
// constructors ignore them.
func WithEnforcerOptions(opts ...metering.EnforcerOption) Option {
	return func(o *options) { o.enforcer = append(o.enforcer, opts...) }
}

// WithFlusherOptions passes opts to NewFlusher, which applies them after the options it
// derives from configuration — so a caller can override anything. The other
// constructors ignore them.
func WithFlusherOptions(opts ...metering.FlusherOption) Option {
	return func(o *options) { o.flusher = append(o.flusher, opts...) }
}
