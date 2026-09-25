package signincfg

import (
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/magiclinks"
	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens"

	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures how NewService assembles the service and the stores it
// builds.
//
// The observability dependencies are options because each one is optional:
// without a logger nothing is logged, without a tracer provider nothing is
// traced, and without a metrics provider nothing is recorded. The registrar and
// the magic-link mailer are options for a different reason. Each is required
// only when its door is on, and NewService refuses an open door that arrives
// without its dependency.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	registrar       signin.Registrar
	magicLinkMailer signin.MagicLinkMailer

	service       []signin.ServiceOption
	refreshTokens []refreshtokens.Option
	magicLinks    []magiclinks.Option
	recoveryCodes []recoverycodes.Option
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

// WithRegistrar supplies the registrar the registration door needs. It must be
// supplied unless Registration.Disabled is set. identity's Service satisfies
// it.
func WithRegistrar(registrar signin.Registrar) Option {
	return func(o *options) { o.registrar = registrar }
}

// WithMagicLinkMailer supplies what delivers a sign-in link, which the
// MagicLinks block needs. It must be supplied exactly when that block is
// present.
func WithMagicLinkMailer(mailer signin.MagicLinkMailer) Option {
	return func(o *options) { o.magicLinkMailer = mailer }
}

// WithServiceOptions passes opts to signin.NewService, applied after the
// options derived from configuration, so a caller can override any of them.
func WithServiceOptions(opts ...signin.ServiceOption) Option {
	return func(o *options) { o.service = append(o.service, opts...) }
}

// WithRefreshTokenStoreOptions passes opts to the refresh token store, applied
// after the options derived from its block, the sweeper included.
func WithRefreshTokenStoreOptions(opts ...refreshtokens.Option) Option {
	return func(o *options) { o.refreshTokens = append(o.refreshTokens, opts...) }
}

// WithMagicLinkStoreOptions passes opts to the sign-in link store, applied
// after the options derived from its block, the sweeper included.
func WithMagicLinkStoreOptions(opts ...magiclinks.Option) Option {
	return func(o *options) { o.magicLinks = append(o.magicLinks, opts...) }
}

// WithRecoveryCodeStoreOptions passes opts to the recovery code store, applied
// after the options derived from its block.
func WithRecoveryCodeStoreOptions(opts ...recoverycodes.Option) Option {
	return func(o *options) { o.recoveryCodes = append(o.recoveryCodes, opts...) }
}
