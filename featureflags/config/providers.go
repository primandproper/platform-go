package featureflagscfg

import (
	"context"
	"net/http"

	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/featureflags"
)

// NewFeatureFlagManager provides a FeatureFlagManager from config.
func NewFeatureFlagManager(ctx context.Context, c *Config, httpClient *http.Client, opts ...Option) (featureflags.FeatureFlagManager, error) {
	o := newOptions(opts)

	circuitBreaker, err := c.CircuitBreaker.NewCircuitBreaker(ctx,
		circuitbreakingcfg.WithLogger(o.logger),
		circuitbreakingcfg.WithMetricsProvider(o.metricsProvider))
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize feature flag circuit breaker")
	}

	return c.NewFeatureFlagManager(ctx, httpClient, circuitBreaker, opts...)
}
