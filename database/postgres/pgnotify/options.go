package pgnotify

import (
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/retry"
)

// Option configures a Listener. The zero configuration works: an absent logger
// logs nowhere, an absent tracer provider traces nowhere, and an absent metrics
// provider records nothing.
type Option func(*options)

type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	rand            retry.Rand
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

// WithLogger attaches a logger. Nothing here fails loudly — a dropped
// connection is retried, a coalesced wake is discarded — so without one, a
// listener that has been flapping for an hour is visible only in metrics.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, which names the span covering
// each connect attempt. Individual notifications are not traced: a root span
// per wake is one span per enqueue across the whole fleet, and the work the
// wake causes belongs to the consumer's trace, not the listener's.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithRand replaces the source that spreads reconnect backoff across the upper
// half of its interval. fn must return a value in [0,1]; a value of 1 yields
// the un-jittered backoff.
//
// The default draws from math/rand/v2 and needs no seeding. A nil fn is
// ignored.
func WithRand(fn retry.Rand) Option {
	return func(o *options) {
		if fn != nil {
			o.rand = fn
		}
	}
}
