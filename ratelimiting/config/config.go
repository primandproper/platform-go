package ratelimitingcfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/ratelimiting"
	"github.com/primandproper/platform-go/v5/ratelimiting/noop"
	redisrl "github.com/primandproper/platform-go/v5/ratelimiting/redis"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	ProviderMemory = "memory"
	ProviderNoop   = "noop"
	ProviderRedis  = "redis"

	defaultRequestsPerSec = 10.0
	defaultBurstSize      = 20
)

// Config configures rate limiting.
type Config struct {
	Provider       string         `env:"PROVIDER"         json:"provider"          yaml:"provider"`
	Redis          redisrl.Config `env:"init"             envPrefix:"REDIS_"       json:"redis"             yaml:"redis"`
	RequestsPerSec float64        `env:"REQUESTS_PER_SEC" json:"requestsPerSecond" yaml:"requestsPerSecond"`
	BurstSize      int            `env:"BURST_SIZE"       json:"burstSize"         yaml:"burstSize"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults sets default values for zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.RequestsPerSec == 0 {
		cfg.RequestsPerSec = defaultRequestsPerSec
	}
	if cfg.BurstSize == 0 {
		cfg.BurstSize = defaultBurstSize
	}
}

// ValidateWithContext validates the config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.RequestsPerSec, validation.Min(0.0)),
		validation.Field(&cfg.BurstSize, validation.Min(0)),
	)
}

// NewRateLimiter returns a RateLimiter from config.
func (cfg *Config) NewRateLimiter(metricsProvider metrics.Provider) (ratelimiting.RateLimiter, error) {
	if cfg == nil {
		return noop.NewRateLimiter(), nil
	}
	cfg.EnsureDefaults()

	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case "", ProviderNoop:
		return noop.NewRateLimiter(), nil
	case ProviderMemory:
		return ratelimiting.NewInMemoryRateLimiter(metricsProvider, cfg.RequestsPerSec, cfg.BurstSize)
	case ProviderRedis:
		return redisrl.NewRedisRateLimiter(cfg.Redis, metricsProvider, cfg.RequestsPerSec, cfg.BurstSize)
	default:
		return nil, errors.Newf("unknown rate limiter provider: %q", cfg.Provider)
	}
}

// NewRateLimiter provides a RateLimiter from config.
func NewRateLimiter(cfg *Config, metricsProvider metrics.Provider) (ratelimiting.RateLimiter, error) {
	if cfg == nil {
		return noop.NewRateLimiter(), nil
	}
	limiter, err := cfg.NewRateLimiter(metricsProvider)
	if err != nil {
		return nil, errors.Wrap(err, "provide rate limiter")
	}
	return limiter, nil
}
