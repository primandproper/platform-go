package routingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v5/encoding"
	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"
	"github.com/primandproper/platform-go/v5/routing"
	"github.com/primandproper/platform-go/v5/routing/backends/chi"
	"github.com/primandproper/platform-go/v5/routing/backends/gin"
	"github.com/primandproper/platform-go/v5/routing/backends/httprouter"
	"github.com/primandproper/platform-go/v5/routing/backends/stdlib"

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

	Chi        *chi.Config        `env:"init"     envPrefix:"CHI_"          json:"chiConfig,omitempty"        yaml:"chiConfig,omitempty"`
	Stdlib     *stdlib.Config     `env:"init"     envPrefix:"STDLIB_"       json:"stdlibConfig,omitempty"     yaml:"stdlibConfig,omitempty"`
	HTTPRouter *httprouter.Config `env:"init"     envPrefix:"HTTPROUTER_"   json:"httpRouterConfig,omitempty" yaml:"httpRouterConfig,omitempty"`
	Gin        *gin.Config        `env:"init"     envPrefix:"GIN_"          json:"ginConfig,omitempty"        yaml:"ginConfig,omitempty"`
	Provider   string             `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a router config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.In(ProviderChi, ProviderStdlib, ProviderHTTPRouter, ProviderGin)),
	)
}

// NewBackend provides a routing.Backend from a routing config, selecting the
// underlying router library by provider.
func NewBackend(cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricProvider metrics.Provider) (routing.Backend, error) {
	switch cfg.Provider {
	case ProviderChi:
		return chi.NewBackend(logger, tracerProvider, metricProvider, cfg.Chi), nil
	case ProviderStdlib:
		return stdlib.NewBackend(logger, tracerProvider, metricProvider, cfg.Stdlib), nil
	case ProviderHTTPRouter:
		return httprouter.NewBackend(logger, tracerProvider, metricProvider, cfg.HTTPRouter), nil
	case ProviderGin:
		return gin.NewBackend(logger, tracerProvider, metricProvider, cfg.Gin), nil
	default:
		return nil, errors.Newf("unknown provider: %s", cfg.Provider)
	}
}

// NewRouter provides a fully-wired *routing.Router from a routing config: it
// selects the backend by provider and layers the declarative Router on top.
func NewRouter(
	cfg *Config,
	enc encoding.ServerEncoderDecoder,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricProvider metrics.Provider,
	opts ...routing.RouterOption,
) (*routing.Router, error) {
	backend, err := NewBackend(cfg, logger, tracerProvider, metricProvider)
	if err != nil {
		return nil, err
	}

	return routing.New(backend, enc, logger, tracerProvider, opts...), nil
}
