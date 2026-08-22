package secretscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/secrets"
	"github.com/primandproper/platform-go/v13/secrets/env"
)

// NewSecretSource provides a SecretSource from config.
func NewSecretSource(ctx context.Context, cfg *Config, opts ...Option) (secrets.SecretSource, error) {
	if cfg == nil {
		o := newOptions(opts)

		// Built into a variable and returned only once its error is known to be
		// nil: env.NewSecretSource returns *env.SecretSource, so returning it
		// straight through would convert a nil pointer into a non-nil
		// secrets.SecretSource on the error path.
		s, err := env.NewSecretSource(env.WithLogger(o.logger), env.WithTracerProvider(o.tracerProvider), env.WithMetricsProvider(o.metricsProvider))
		if err != nil {
			return nil, err
		}

		return s, nil
	}

	source, err := cfg.NewSecretSource(ctx, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "provide secret source")
	}

	return source, nil
}
