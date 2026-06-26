package secretscfg

import (
	"context"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"
	"github.com/primandproper/platform-go/secrets"
	"github.com/primandproper/platform-go/secrets/env"
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
