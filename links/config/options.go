package linkscfg

import (
	"github.com/primandproper/platform-go/v14/links"
	linksdatabase "github.com/primandproper/platform-go/v14/links/database"

	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
)

// Option configures how NewMinter assembles its Minter.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. Requiring them positionally made a caller that wanted none of the
// three name all three anyway, usually as noops.
type Option func(*options)

// options collects what the options set. The two pass-through slices exist
// because Go allows one variadic per function and that slot belongs to this
// package's own Option; anything bound for a component this constructor builds
// arrives through a WithXOptions instead.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	minter        []links.Option
	databaseStore []linksdatabase.Option
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

// WithMinterOptions passes opts to the Minter, which applies them after the
// options it derives from configuration — so a caller can override anything,
// and can register actions beyond those in the file.
//
// Registering actions in code is the right move when their URLs are assembled
// from something the application already knows — a base URL it also serves
// from, a per-tenant host — rather than written out. Config.Actions is for the
// ones that are written out.
//
// They cannot be a second variadic on NewMinter: Go allows one per function,
// and that slot is what makes the observability optional.
func WithMinterOptions(opts ...links.Option) Option {
	return func(o *options) { o.minter = append(o.minter, opts...) }
}

// WithDatabaseStoreOptions passes options through to the store — WithCodec,
// most usefully. They are applied after the ones derived from the Config, so
// they win.
func WithDatabaseStoreOptions(opts ...linksdatabase.Option) Option {
	return func(o *options) { o.databaseStore = append(o.databaseStore, opts...) }
}
