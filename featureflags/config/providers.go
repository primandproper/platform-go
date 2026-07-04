package featureflagscfg

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/featureflags"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	"github.com/primandproper/platform-go/v3/observability/tracing"
)

// ProvideFeatureFlagManager provides a FeatureFlagManager from config.
func ProvideFeatureFlagManager(ctx context.Context, c *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, httpClient *http.Client) (featureflags.FeatureFlagManager, error) {
	circuitBreaker, err := c.CircuitBreaker.ProvideCircuitBreaker(ctx, logger, metricsProvider)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize feature flag circuit breaker")
	}

	return c.ProvideFeatureFlagManager(logger, tracerProvider, metricsProvider, httpClient, circuitBreaker)
}
