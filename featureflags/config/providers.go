package featureflagscfg

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/featureflags"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"
)

// NewFeatureFlagManager provides a FeatureFlagManager from config.
func NewFeatureFlagManager(ctx context.Context, c *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, httpClient *http.Client) (featureflags.FeatureFlagManager, error) {
	circuitBreaker, err := c.CircuitBreaker.NewCircuitBreaker(ctx, logger, metricsProvider)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize feature flag circuit breaker")
	}

	return c.NewFeatureFlagManager(logger, tracerProvider, metricsProvider, httpClient, circuitBreaker)
}
