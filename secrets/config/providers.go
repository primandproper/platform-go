package secretscfg

import (
	"context"

	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"
	"github.com/primandproper/platform-go/v4/secrets"
	"github.com/primandproper/platform-go/v4/secrets/env"
)

// NewSecretSource provides a SecretSource from config.
func NewSecretSource(ctx context.Context, cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (secrets.SecretSource, error) {
	if cfg == nil {
		return env.NewEnvSecretSource(logger, tracerProvider, metricsProvider)
	}
	source, err := cfg.NewSecretSource(ctx, logger, tracerProvider, metricsProvider)
	if err != nil {
		return nil, errors.Wrap(err, "provide secret source")
	}
	return source, nil
}
