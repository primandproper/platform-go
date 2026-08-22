package emailcfg

import (
	"context"
	"net/http"

	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/email"
	"github.com/primandproper/platform-go/v13/errors"
)

// NewEmailer provides an email.Emailer from a config.
func NewEmailer(ctx context.Context, cfg *Config, client *http.Client, opts ...Option) (email.Emailer, error) {
	o := newOptions(opts)

	circuitBreaker, err := cfg.CircuitBreaker.NewCircuitBreaker(ctx,
		circuitbreakingcfg.WithLogger(o.logger),
		circuitbreakingcfg.WithMetricsProvider(o.metricsProvider))
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize email circuit breaker")
	}

	return cfg.NewEmailer(ctx, client, circuitBreaker, opts...)
}
