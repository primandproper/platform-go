package emailcfg

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v3/email"
	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	"github.com/primandproper/platform-go/v3/observability/tracing"
)

// ProvideEmailer provides an email.Emailer from a config.
func ProvideEmailer(ctx context.Context, cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, client *http.Client) (email.Emailer, error) {
	circuitBreaker, err := cfg.CircuitBreaker.ProvideCircuitBreaker(ctx, logger, metricsProvider)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize email circuit breaker")
	}

	return cfg.ProvideEmailer(ctx, logger, tracerProvider, client, circuitBreaker, metricsProvider)
}
