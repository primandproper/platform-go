package dataprivacycfg

import (
	"github.com/primandproper/platform-go/v13/dataprivacy"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
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
// others, so a single wiring site can carry options for whichever component it
// happens to build. They cannot be a second variadic on the constructor: Go
// allows one per function, and that slot is what makes the observability
// optional.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	store     []dataprivacy.SQLStoreOption
	service   []dataprivacy.ServiceOption
	fulfiller []dataprivacy.FulfillerOption
	sweeper   []dataprivacy.SweeperOption
	urlSigner []dataprivacy.URLSignerOption
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

// WithStoreOptions passes opts to NewStore, which applies them after the options it
// derives from configuration — so a caller can override anything. The other
// constructors ignore them.
func WithStoreOptions(opts ...dataprivacy.SQLStoreOption) Option {
	return func(o *options) { o.store = append(o.store, opts...) }
}

// WithServiceOptions passes opts to NewService, which applies them after the options it
// derives from configuration — so a caller can override anything. The other
// constructors ignore them.
func WithServiceOptions(opts ...dataprivacy.ServiceOption) Option {
	return func(o *options) { o.service = append(o.service, opts...) }
}

// WithFulfillerOptions passes opts to NewFulfiller, which applies them after the options it
// derives from configuration — so a caller can override anything. The other
// constructors ignore them.
func WithFulfillerOptions(opts ...dataprivacy.FulfillerOption) Option {
	return func(o *options) { o.fulfiller = append(o.fulfiller, opts...) }
}

// WithSweeperOptions passes opts to NewSweeper, which applies them after the options it
// derives from configuration — so a caller can override anything. The other
// constructors ignore them.
func WithSweeperOptions(opts ...dataprivacy.SweeperOption) Option {
	return func(o *options) { o.sweeper = append(o.sweeper, opts...) }
}

// WithURLSignerOptions passes opts to the NewArtifactURLSigner this package
// builds when it is given an upload manager. It exists for the clock: a caller
// that hands the Fulfiller a WithFulfillerClock and does not hand the signer the
// same one gets a notification whose stated expiry is in wall-clock time while
// everything around it is not.
func WithURLSignerOptions(opts ...dataprivacy.URLSignerOption) Option {
	return func(o *options) { o.urlSigner = append(o.urlSigner, opts...) }
}
