package auditcfg

import (
	"github.com/primandproper/platform-go/v10/audit"
	"github.com/primandproper/platform-go/v10/observability"
	"github.com/primandproper/platform-go/v10/observability/logging"
	"github.com/primandproper/platform-go/v10/observability/metrics"
	"github.com/primandproper/platform-go/v10/observability/tracing"
)

// Option configures how this package's constructors assemble what they build.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. Requiring them positionally made a caller that wanted none of the
// three name all three anyway, usually as noops.
//
// The passthrough options each apply to one constructor and are ignored by the
// others, so a single wiring site can carry options for whichever of the three
// components it happens to build. A constructor cannot take them as its own
// variadic instead — Go allows one variadic per function, and that slot is what
// makes the observability optional.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	recorder []audit.RecorderOption
	reader   []audit.ReaderOption
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

// WithRecorderOptions passes opts to NewRecorder, which applies them after the
// options it derives from configuration — so a caller can override anything,
// and can register redactions beyond those in the file. Other constructors
// ignore them.
func WithRecorderOptions(opts ...audit.RecorderOption) Option {
	return func(o *options) { o.recorder = append(o.recorder, opts...) }
}

// WithReaderOptions passes opts to NewReader, which applies them after the
// options it derives from configuration. Other constructors ignore them.
func WithReaderOptions(opts ...audit.ReaderOption) Option {
	return func(o *options) { o.reader = append(o.reader, opts...) }
}

// There is no passthrough for the retention target. audit.PruneTarget is a
// value with exported fields and no options of its own, and the sweep that
// drives it belongs to a retention.Sweeper — whose options are
// retentioncfg's to pass.
