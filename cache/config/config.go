package config

import (
	"context"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v6/cache"
	"github.com/primandproper/platform-go/v6/cache/memory"
	"github.com/primandproper/platform-go/v6/cache/redis"
	circuitbreakingcfg "github.com/primandproper/platform-go/v6/circuitbreaking/config"
	"github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderMemory is the memory provider.
	ProviderMemory = "memory"
	// ProviderRedis is the redis provider.
	ProviderRedis = "redis"
)

type (
	// Config is the configuration for the cache.
	Config struct {
		Redis          *redis.Config             `env:"init"     envPrefix:"REDIS_"            json:"redis"                yaml:"redis"`
		Provider       string                    `env:"PROVIDER" json:"provider"               yaml:"provider"`
		CircuitBreaker circuitbreakingcfg.Config `env:"init"     envPrefix:"CIRCUIT_BREAKING_" json:"circuitBreakerConfig" yaml:"circuitBreakerConfig"`
		Expiry         time.Duration             `env:"EXPIRY"   envDefault:"1h"               json:"expiry"               yaml:"expiry"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.In(ProviderMemory, ProviderRedis)),
		validation.Field(&cfg.Redis, validation.When(cfg.Provider == ProviderRedis, validation.Required)),
	)
}

// NewCache provides a Cache.
func NewCache[T any](ctx context.Context, cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (cache.Cache[T], error) {
	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case ProviderMemory:
		return memory.NewInMemoryCache[T](logger, tracerProvider, metricsProvider)
	case ProviderRedis:
		cb, err := cfg.CircuitBreaker.NewCircuitBreaker(ctx, logger, metricsProvider)
		if err != nil {
			return nil, errors.Wrap(err, "initializing cache circuit breaker")
		}
		expiry := cfg.Expiry
		if expiry <= 0 {
			expiry = time.Hour
		}
		return redis.NewRedisCache[T](cfg.Redis, expiry, logger, tracerProvider, metricsProvider, cb)
	default:
		return nil, errors.Newf("invalid cache provider: %q", cfg.Provider)
	}
}
