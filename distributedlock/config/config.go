package distributedlockcfg

import (
	"context"
	"strings"

	circuitbreakingcfg "github.com/primandproper/platform-go/v5/circuitbreaking/config"
	"github.com/primandproper/platform-go/v5/database"
	"github.com/primandproper/platform-go/v5/distributedlock"
	"github.com/primandproper/platform-go/v5/distributedlock/memory"
	"github.com/primandproper/platform-go/v5/distributedlock/noop"
	pglock "github.com/primandproper/platform-go/v5/distributedlock/postgres"
	redislock "github.com/primandproper/platform-go/v5/distributedlock/redis"
	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// RedisProvider selects the redis-backed distributedlock.Locker implementation.
	RedisProvider = "redis"
	// PostgresProvider selects the postgres-backed distributedlock.Locker implementation.
	PostgresProvider = "postgres"
	// MemoryProvider selects the in-memory distributedlock.Locker implementation.
	MemoryProvider = "memory"
	// NoopProvider selects the no-op distributedlock.Locker implementation.
	NoopProvider = "noop"
)

// Config dispatches to a distributedlock provider implementation.
type Config struct {
	_              struct{}                  `json:"-"       yaml:"-"`
	Redis          *redislock.Config         `env:"init"     envPrefix:"REDIS_"            json:"redis"                yaml:"redis"`
	Postgres       *pglock.Config            `env:"init"     envPrefix:"POSTGRES_"         json:"postgres"             yaml:"postgres"`
	Provider       string                    `env:"PROVIDER" json:"provider"               yaml:"provider"`
	CircuitBreaker circuitbreakingcfg.Config `env:"init"     envPrefix:"CIRCUIT_BREAKING_" json:"circuitBreakerConfig" yaml:"circuitBreakerConfig"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct. Empty Provider is acceptable and
// resolves to the noop locker — matching the dispatch convention used elsewhere
// in platform.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.In(RedisProvider, PostgresProvider, MemoryProvider, NoopProvider)),
		validation.Field(&cfg.Redis, validation.When(cfg.Provider == RedisProvider, validation.Required)),
		validation.Field(&cfg.Postgres, validation.When(cfg.Provider == PostgresProvider, validation.Required)),
	)
}

// NewLocker constructs a distributedlock.Locker for the configured provider.
// The db argument is required only when Provider is PostgresProvider; pass nil
// otherwise. Unknown or empty providers fall back to the noop locker.
func NewLocker(
	ctx context.Context,
	cfg *Config,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
	db database.Client,
) (distributedlock.Locker, error) {
	if cfg == nil {
		return nil, distributedlock.ErrNilConfig
	}

	circuitBreaker, err := circuitbreakingcfg.NewCircuitBreaker(ctx, &cfg.CircuitBreaker, logger, metricsProvider)
	if err != nil {
		return nil, errors.Wrap(err, "initializing distributedlock circuit breaker")
	}

	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case RedisProvider:
		return redislock.NewRedisLocker(cfg.Redis, logger, tracerProvider, metricsProvider, circuitBreaker)
	case PostgresProvider:
		return pglock.NewPostgresLocker(cfg.Postgres, db, logger, tracerProvider, metricsProvider, circuitBreaker)
	case MemoryProvider:
		return memory.NewLocker(logger, tracerProvider, metricsProvider)
	default:
		return noop.NewLocker(), nil
	}
}
