package webauthn

import (
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures the RelyingParty this package constructs. The zero
// configuration works: an absent logger logs nowhere, an absent tracer provider
// traces nowhere, and an absent metrics provider records nothing.
type Option func(*options)

type options struct {
	clock           clock.Clock
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// newOptions applies opts over the defaults, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{clock: clock.NewClock()}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithLogger attaches a logger.
//
// Worth setting. A ceremony that fails verification is a security-relevant
// event — a challenge answered from an origin that is not configured, a
// credential presented by a user who does not own it — and without a logger the
// only trace it leaves is whatever the caller does with the returned error.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every
// ceremony step.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the ceremony counters and
// latency histogram. An absent provider records nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithClock swaps the clock a ceremony's remaining life is measured against
// when its state is stored.
//
// The deadline itself comes from the library, which reads the wall clock either
// way, so this does not move a ceremony's expiry — it decides how much of that
// expiry the store is told about.
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.clock = c
		}
	}
}
