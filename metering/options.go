package metering

import (
	"github.com/primandproper/platform-go/v13/analytics"
	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// EnforcerOption configures a QuotaEnforcer.
type EnforcerOption func(*QuotaEnforcer)

// WithEnforcerClock swaps the clock deciding which period "now" falls in and how
// long a cached total lives.
func WithEnforcerClock(c clock.Clock) EnforcerOption {
	return func(e *QuotaEnforcer) {
		if c != nil {
			e.clock = c
		}
	}
}

// WithEnforcerCache attaches the cache Check reads through.
//
// Without one, Check reads the durable total on every call. That is correct — and
// it is the thing this package warns about at length, because a durable read on
// every request is how metering becomes the latency bottleneck of the system it
// was added to measure. An enforcer with no cache logs that it has none at
// construction, so the omission is noticed before production does.
func WithEnforcerCache(totals cache.Cache[CachedTotal]) EnforcerOption {
	return func(e *QuotaEnforcer) {
		if totals != nil {
			e.totals = totals
		}
	}
}

// WithEnforcerQuotaSource swaps where a subject's limits come from. Defaults to
// the Registry's static quotas — see NewRegistryQuotaSource for when that is the
// wrong answer.
func WithEnforcerQuotaSource(quotas QuotaSource) EnforcerOption {
	return func(e *QuotaEnforcer) {
		if quotas != nil {
			e.quotas = quotas
		}
	}
}

// WithEnforcerPeriodResolver swaps the resolver deciding which window a decision
// is about.
//
// It must be the same resolver the Recorder uses. Two resolvers that disagree
// about where a period begins would have the enforcer reading a total the
// recorder is not writing to, which presents as a quota that never fills.
func WithEnforcerPeriodResolver(resolver PeriodResolver) EnforcerOption {
	return func(e *QuotaEnforcer) {
		if resolver != nil {
			e.resolver = resolver
		}
	}
}

// WithEnforcerLogger attaches a logger.
func WithEnforcerLogger(logger logging.Logger) EnforcerOption {
	return func(e *QuotaEnforcer) {
		e.logger = logger
	}
}

// WithEnforcerTracerProvider attaches a tracer provider.
func WithEnforcerTracerProvider(tracerProvider tracing.Provider) EnforcerOption {
	return func(e *QuotaEnforcer) {
		e.tracerProvider = tracerProvider
	}
}

// WithEnforcerMetricsProvider attaches a metrics provider.
func WithEnforcerMetricsProvider(metricsProvider metrics.Provider) EnforcerOption {
	return func(e *QuotaEnforcer) {
		e.metricsProvider = metricsProvider
	}
}

// PlanLimitOption configures a PlanLimitSource.
type PlanLimitOption func(*PlanLimitSource)

// WithPlanLimitLogger attaches a logger. A subject entitled to a product the
// limits table does not name is reported through it and nowhere else — the
// request succeeds, on the unsubscribed limit, because a customer is not the
// right person to tell about a plan somebody forgot to configure — so without
// one, a tier enforcing the wrong number is visible only in metrics.
func WithPlanLimitLogger(logger logging.Logger) PlanLimitOption {
	return func(s *PlanLimitSource) {
		s.logger = logger
	}
}

// WithPlanLimitTracerProvider attaches a tracer provider. The span it produces
// is where the EntitlementReader's lookup shows up — the one piece of I/O a
// quota resolution does, and otherwise time the enforcer's own span cannot
// account for.
func WithPlanLimitTracerProvider(tracerProvider tracing.Provider) PlanLimitOption {
	return func(s *PlanLimitSource) {
		s.tracerProvider = tracerProvider
	}
}

// WithPlanLimitMetricsProvider attaches a metrics provider, enabling the
// unconfigured-product counter — which is the instrument worth alerting on here,
// because anything it counts is a customer being enforced against the wrong
// number.
func WithPlanLimitMetricsProvider(metricsProvider metrics.Provider) PlanLimitOption {
	return func(s *PlanLimitSource) {
		s.metricsProvider = metricsProvider
	}
}

// FlusherOption configures a Flusher.
type FlusherOption func(*Flusher)

// WithFlusherClock swaps the clock driving leases and backoff.
func WithFlusherClock(c clock.Clock) FlusherOption {
	return func(f *Flusher) {
		if c != nil {
			f.clock = c
		}
	}
}

// WithFlusherLogger attaches a logger. A total that has exhausted its attempts is
// reported through it and nowhere else — there is no caller to return it to — so
// without one, usage that will never be billed is visible only in metrics.
func WithFlusherLogger(logger logging.Logger) FlusherOption {
	return func(f *Flusher) {
		f.logger = logger
	}
}

// WithFlusherTracerProvider attaches a tracer provider.
func WithFlusherTracerProvider(tracerProvider tracing.Provider) FlusherOption {
	return func(f *Flusher) {
		f.tracerProvider = tracerProvider
	}
}

// WithFlusherMetricsProvider attaches a metrics provider, enabling the backlog
// gauge — which is the one instrument in this package worth alerting on.
func WithFlusherMetricsProvider(metricsProvider metrics.Provider) FlusherOption {
	return func(f *Flusher) {
		f.metricsProvider = metricsProvider
	}
}

// RecorderOption configures a DurableRecorder.
type RecorderOption func(*DurableRecorder)

// WithRecorderClock swaps the clock stamping ingest and resolving "now" for
// usage that carries no event time.
func WithRecorderClock(c clock.Clock) RecorderOption {
	return func(r *DurableRecorder) {
		if c != nil {
			r.clock = c
		}
	}
}

// WithRecorderPeriodResolver swaps the resolver deciding which window usage lands
// in. Defaults to NewCalendarPeriodResolver(nil), which answers the calendar
// periods and refuses PeriodBillingPeriod.
func WithRecorderPeriodResolver(resolver PeriodResolver) RecorderOption {
	return func(r *DurableRecorder) {
		if resolver != nil {
			r.resolver = resolver
		}
	}
}

// WithRecorderLogger attaches a logger. A dropped record is reported through it
// and nowhere else — the caller is told the batch succeeded, because the record
// that named an unknown meter is not the caller's to fix — so without one, usage
// silently going nowhere is visible only in metrics.
func WithRecorderLogger(logger logging.Logger) RecorderOption {
	return func(r *DurableRecorder) {
		r.logger = logger
	}
}

// WithRecorderTracerProvider attaches a tracer provider.
func WithRecorderTracerProvider(tracerProvider tracing.Provider) RecorderOption {
	return func(r *DurableRecorder) {
		r.tracerProvider = tracerProvider
	}
}

// WithRecorderMetricsProvider attaches a metrics provider.
func WithRecorderMetricsProvider(metricsProvider metrics.Provider) RecorderOption {
	return func(r *DurableRecorder) {
		r.metricsProvider = metricsProvider
	}
}

// WithRecorderAnalytics emits an event per accepted record to an analytics
// reporter, so usage patterns are queryable outside the billing system.
//
// It is off by default and should stay off for high-volume meters. A meter that
// fires on every API request would post one analytics event per request, which
// is a second data pipeline the size of the first — and analytics warehouses
// charge by the row. Dimensions are what make it worth turning on for the meters
// where it is: the aggregate knows the total, and only the events know it was all
// one model in one region.
func WithRecorderAnalytics(reporter analytics.EventReporter) RecorderOption {
	return func(r *DurableRecorder) {
		if reporter != nil {
			r.analytics = reporter
		}
	}
}

// SQLStoreOption configures a SQL Store.
type SQLStoreOption func(*SQLStore)

// WithTablePrefix overrides DefaultTablePrefix. It must be a plain SQL identifier
// fragment: it is interpolated into the query text, not bound as a parameter, and
// it must match the prefix the migrations were rendered with.
func WithTablePrefix(prefix string) SQLStoreOption {
	return func(s *SQLStore) {
		if prefix != "" {
			s.tables = newTables(prefix)
		}
	}
}

// WithStoreLogger attaches a logger.
func WithStoreLogger(logger logging.Logger) SQLStoreOption {
	return func(s *SQLStore) {
		s.logger = logger
	}
}

// WithStoreTracerProvider attaches a tracer provider.
func WithStoreTracerProvider(tracerProvider tracing.Provider) SQLStoreOption {
	return func(s *SQLStore) {
		s.tracerProvider = tracerProvider
	}
}

// WithStoreMetricsProvider attaches a metrics provider.
func WithStoreMetricsProvider(metricsProvider metrics.Provider) SQLStoreOption {
	return func(s *SQLStore) {
		s.metricsProvider = metricsProvider
	}
}
