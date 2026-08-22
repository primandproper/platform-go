package circuitbreakingcfg

import (
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// NameAttributeKey is the metric attribute carrying which breaker a measurement
// came from. It is exported because a dashboard querying these counters has to
// spell it, and a constant is better than a string repeated in a query builder.
const NameAttributeKey = "circuit_breaker.name"

// Option customizes how a CircuitBreaker is provided.
//
// The observability dependencies are options rather than parameters because
// both are genuinely optional: an absent logger logs nowhere and an absent
// metrics provider records nothing. Requiring them positionally made a caller
// that wanted neither name both anyway, usually as noops.
//
// There is no tracer provider here. A circuit breaker's decisions are counters
// and log lines, not spans — it has nothing to trace.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger           logging.Logger
	metricsProvider  metrics.Provider
	metricAttributes []attribute.KeyValue
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

// addOptions returns the attributes every measurement this breaker records
// carries: its name, plus whatever the caller added.
//
// The name is an attribute rather than part of the instrument name so that one
// breaker's trips can be read on their own and every breaker's trips can be read
// together, and so that a breaker nobody named does not mint an instrument
// nobody was expecting.
func (o *options) addOptions(name string) []metric.AddOption {
	attrs := make([]attribute.KeyValue, 0, len(o.metricAttributes)+1)
	attrs = append(attrs, attribute.String(NameAttributeKey, name))
	attrs = append(attrs, o.metricAttributes...)

	return []metric.AddOption{metric.WithAttributes(attrs...)}
}

// WithMetricAttributes attaches a fixed set of attributes to every metric the
// circuit breaker emits. It is used to distinguish breakers that share counter
// names (for example, tagging a per-key breaker with its partition).
func WithMetricAttributes(attrs ...attribute.KeyValue) Option {
	return func(o *options) {
		o.metricAttributes = append(o.metricAttributes, attrs...)
	}
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithMetricsProvider attaches a metrics provider for the breaker's tripped,
// failed, and reset counters. An absent provider records nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithPillars attaches a logger and metrics provider in one go, for the common
// case where a caller has already built them together. A nil Pillars attaches
// nothing. The pillars' tracer provider is ignored — see Option.
//
// It is applied in order with the individual options, so a caller can hand over
// its pillars and then override one of them.
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) {
		logger, _, metricsProvider := p.Deps()
		o.logger, o.metricsProvider = logger, metricsProvider
	}
}
