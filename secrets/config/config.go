package secretscfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	"github.com/primandproper/platform-go/v3/observability/tracing"
	"github.com/primandproper/platform-go/v3/secrets"
	"github.com/primandproper/platform-go/v3/secrets/env"
	"github.com/primandproper/platform-go/v3/secrets/gcp"
	"github.com/primandproper/platform-go/v3/secrets/kubectl"
	"github.com/primandproper/platform-go/v3/secrets/noop"
	"github.com/primandproper/platform-go/v3/secrets/ssm"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderEnv represents environment variables.
	ProviderEnv = "env"
	// ProviderNoop represents the noop provider.
	ProviderNoop = "noop"
	// ProviderGCP represents GCP Secret Manager.
	ProviderGCP = "gcp"
	// ProviderSSM represents AWS SSM Parameter Store.
	ProviderSSM = "ssm"
	// ProviderKubectl represents Kubernetes secrets.
	ProviderKubectl = "kubectl"
)

// Config configures secret source selection.
type Config struct {
	GCPClient     gcp.SecretVersionAccessor `json:"-"`
	SSMClient     ssm.GetParameterAPI       `json:"-"`
	KubectlClient kubectl.SecretGetter      `json:"-"`
	Env           *env.Config               `env:"init"     envPrefix:"ENV_"     json:"env,omitempty"`
	GCP           *gcp.Config               `env:"init"     envPrefix:"GCP_"     json:"gcp,omitempty"`
	SSM           *ssm.Config               `env:"init"     envPrefix:"SSM_"     json:"ssm,omitempty"`
	Kubectl       *kubectl.Config           `env:"init"     envPrefix:"KUBECTL_" json:"kubectl,omitempty"`
	Provider      string                    `env:"PROVIDER" json:"provider"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.In(ProviderEnv, ProviderNoop, ProviderGCP, ProviderSSM, ProviderKubectl, "")),
		validation.Field(&cfg.GCP, validation.When(cfg.Provider == ProviderGCP, validation.Required), validation.When(cfg.Provider != ProviderGCP, validation.Nil)),
		validation.Field(&cfg.SSM, validation.When(cfg.Provider == ProviderSSM, validation.Required), validation.When(cfg.Provider != ProviderSSM, validation.Nil)),
		validation.Field(&cfg.Kubectl, validation.When(cfg.Provider == ProviderKubectl, validation.Required), validation.When(cfg.Provider != ProviderKubectl, validation.Nil)),
	)
}

// ProvideSecretSource returns a SecretSource from config.
func (cfg *Config) ProvideSecretSource(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (secrets.SecretSource, error) {
	if cfg == nil {
		return env.NewEnvSecretSource(logger, tracerProvider, metricsProvider)
	}

	provider := strings.TrimSpace(strings.ToLower(cfg.Provider))
	switch provider {
	case "", ProviderEnv:
		return env.NewEnvSecretSource(logger, tracerProvider, metricsProvider)
	case ProviderNoop:
		return noop.NewSecretSource(), nil
	case ProviderGCP:
		if cfg.GCP == nil {
			return nil, errors.New("gcp provider requires gcp config")
		}
		return gcp.NewGCPSecretSource(ctx, cfg.GCP, cfg.GCPClient, logger, tracerProvider, metricsProvider)
	case ProviderSSM:
		if cfg.SSM == nil {
			return nil, errors.New("ssm provider requires ssm config")
		}
		return ssm.NewSSMSecretSource(ctx, cfg.SSM, cfg.SSMClient, logger, tracerProvider, metricsProvider)
	case ProviderKubectl:
		if cfg.Kubectl == nil {
			return nil, errors.New("kubectl provider requires kubectl config")
		}
		return kubectl.NewKubectlSecretSource(ctx, cfg.Kubectl, cfg.KubectlClient, logger, tracerProvider, metricsProvider)
	default:
		return nil, errors.Newf("unknown secret source provider: %q", cfg.Provider)
	}
}
