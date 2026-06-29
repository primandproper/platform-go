package secretscfg

import (
	"context"

	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"
	"github.com/primandproper/platform-go/v2/secrets"
	"github.com/primandproper/platform-go/v2/secrets/env"
)

// ProvideSecretSourceFromConfig provides a SecretSource from config.
func ProvideSecretSourceFromConfig(ctx context.Context, cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (secrets.SecretSource, error) {
	if cfg == nil {
		return env.NewEnvSecretSource(logger, tracerProvider, metricsProvider)
	}
	source, err := cfg.ProvideSecretSource(ctx, logger, tracerProvider, metricsProvider)
	if err != nil {
		return nil, errors.Wrap(err, "provide secret source")
	}
	return source, nil
}
