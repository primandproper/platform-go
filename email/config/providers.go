package emailcfg

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v6/email"
	"github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"
)

// NewEmailer provides an email.Emailer from a config.
func NewEmailer(ctx context.Context, cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, client *http.Client) (email.Emailer, error) {
	circuitBreaker, err := cfg.CircuitBreaker.NewCircuitBreaker(ctx, logger, metricsProvider)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize email circuit breaker")
	}

	return cfg.NewEmailer(ctx, logger, tracerProvider, client, circuitBreaker, metricsProvider)
}
