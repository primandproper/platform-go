// Package routingcfg selects and builds a routing backend from configuration:
// chi, net/http's ServeMux, httprouter, or gin.
//
// Every backend requires a service name, and those rules are enforced from here
// rather than left to the backend: a router built from a config that named only
// a provider used to run with an empty service name on all of its spans, which
// is the kind of thing nobody notices until they go looking for the traces.
package routingcfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/chi"
	"github.com/primandproper/platform-go/v13/routing/backends/gin"
	"github.com/primandproper/platform-go/v13/routing/backends/httprouter"
	"github.com/primandproper/platform-go/v13/routing/backends/stdlib"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderChi is the string we use to refer to chi.
	ProviderChi = "chi"
	// ProviderStdlib is the string we use to refer to the net/http.ServeMux backend.
	ProviderStdlib = "stdlib"
	// ProviderHTTPRouter is the string we use to refer to the julienschmidt/httprouter backend.
	ProviderHTTPRouter = "httprouter"
	// ProviderGin is the string we use to refer to the gin-gonic/gin backend.
	ProviderGin = "gin"
)

// Config configures our router.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	Chi        *chi.Config        `env:",init"    envPrefix:"CHI_"          json:"chiConfig,omitempty"        yaml:"chiConfig,omitempty"`
	Stdlib     *stdlib.Config     `env:",init"    envPrefix:"STDLIB_"       json:"stdlibConfig,omitempty"     yaml:"stdlibConfig,omitempty"`
	HTTPRouter *httprouter.Config `env:",init"    envPrefix:"HTTPROUTER_"   json:"httpRouterConfig,omitempty" yaml:"httpRouterConfig,omitempty"`
	Gin        *gin.Config        `env:",init"    envPrefix:"GIN_"          json:"ginConfig,omitempty"        yaml:"ginConfig,omitempty"`
	Provider   string             `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
}

// providers are every backend this package implements. Validation and NewBackend
// both read it.
var providers = []string{ProviderChi, ProviderStdlib, ProviderHTTPRouter, ProviderGin}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a router config struct.
//
// Provider is Required as well as constrained: ozzo's In skips empty values, so
// without it an unset provider validated cleanly and then matched no dispatch
// case.
//
// The selected backend's own config is required and validated, and the others
// are skipped. Every backend requires a ServiceName and every one of those
// rules was unreachable, because nothing named the sub-config fields here and
// nothing called this from NewBackend — a router built from a config that named
// only a provider ran with an empty service name on all of its spans. The
// unselected backends are skipped rather than merely unguarded because ozzo
// validates any non-nil pointer to a Validatable once a field's rules have run,
// and `env:",init"` leaves all four non-nil.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	provider := cfgnorm.Provider(cfg.Provider)

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			// Checked normalized, matching dispatch. Dispatch itself compared
			// the raw string, so "Chi" reached neither.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "routing provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.Chi, validation.Skip.When(provider != ProviderChi), validation.Required),
		validation.Field(&cfg.Stdlib, validation.Skip.When(provider != ProviderStdlib), validation.Required),
		validation.Field(&cfg.HTTPRouter, validation.Skip.When(provider != ProviderHTTPRouter), validation.Required),
		validation.Field(&cfg.Gin, validation.Skip.When(provider != ProviderGin), validation.Required),
	)
}

// NewBackend provides a routing.Backend from a routing config, selecting the
// underlying router library by provider.
func NewBackend(ctx context.Context, cfg *Config, opts ...Option) (routing.Backend, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "routing provider")
	if err != nil {
		return nil, err
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating routing config")
	}

	switch provider {
	case ProviderChi:
		return chi.NewBackend(cfg.Chi, chi.WithLogger(logger), chi.WithTracerProvider(tracerProvider), chi.WithMetricsProvider(metricProvider)), nil
	case ProviderStdlib:
		return stdlib.NewBackend(cfg.Stdlib, stdlib.WithLogger(logger), stdlib.WithTracerProvider(tracerProvider), stdlib.WithMetricsProvider(metricProvider)), nil
	case ProviderHTTPRouter:
		return httprouter.NewBackend(cfg.HTTPRouter, httprouter.WithLogger(logger), httprouter.WithTracerProvider(tracerProvider), httprouter.WithMetricsProvider(metricProvider)), nil
	case ProviderGin:
		return gin.NewBackend(cfg.Gin, gin.WithLogger(logger), gin.WithTracerProvider(tracerProvider), gin.WithMetricsProvider(metricProvider)), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "routing provider %q", cfg.Provider)
	}
}

// NewRouter provides a fully-wired *routing.Router from a routing config: it
// selects the backend by provider and layers the declarative Router on top.
func NewRouter(
	ctx context.Context,
	cfg *Config,
	enc encoding.ServerEncoderDecoder,
	opts ...Option,
) (*routing.Router, error) {
	o := newOptions(opts)

	backend, err := NewBackend(ctx, cfg, opts...)
	if err != nil {
		return nil, err
	}

	routerOpts := append([]routing.RouterOption{routing.WithLogger(o.logger), routing.WithTracerProvider(o.tracerProvider)}, o.router...)

	return routing.New(backend, enc, routerOpts...), nil
}
