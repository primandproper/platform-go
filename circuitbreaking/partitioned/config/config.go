package partitionedcfg

import (
	"context"

	"github.com/primandproper/platform-go/v4/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v4/circuitbreaking/config"
	"github.com/primandproper/platform-go/v4/circuitbreaking/partitioned"
	"github.com/primandproper/platform-go/v4/circuitbreaking/partitioned/noop"
	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"go.opentelemetry.io/otel/attribute"
)

const (
	// partitionAttributeKey is the metric attribute used to distinguish breakers that share counter names.
	partitionAttributeKey = "partition"

	// globalPartition is the partition attribute value used for the shared fallback breaker.
	globalPartition = "global"
)

// Config configures a partitioned (keyed) circuit breaker.
type Config struct {
	Keys []string                  `env:"KEYS" json:"circuitBreakerKeys"`
	Base circuitbreakingcfg.Config `env:"init" envPrefix:"BASE_"         json:"base"`
}

// EnsureDefaults ensures the config has sane defaults.
func (cfg *Config) EnsureDefaults() {
	cfg.Base.EnsureDefaults()
}

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := cfg.Base.ValidateWithContext(ctx); err != nil {
		return err
	}

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Keys, validation.Each(validation.Required)),
	)
}

// NewKeyedCircuitBreaker provides a KeyedCircuitBreaker.
func (cfg *Config) NewKeyedCircuitBreaker(ctx context.Context, logger logging.Logger, metricsProvider metrics.Provider) (partitioned.KeyedCircuitBreaker, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	logger = logging.EnsureLogger(logger)

	if err := cfg.ValidateWithContext(ctx); err != nil {
		logger.Error("invalid config passed, providing noop keyed circuit breaker", err)
		return noop.NewKeyedCircuitBreaker(), nil
	}

	cfg.EnsureDefaults()

	global, err := cfg.Base.NewCircuitBreaker(ctx, logger, metricsProvider, circuitbreakingcfg.WithMetricAttributes(attribute.String(partitionAttributeKey, globalPartition)))
	if err != nil {
		return nil, errors.Wrap(err, "providing global circuit breaker")
	}

	breakers := make(map[string]circuitbreaking.CircuitBreaker, len(cfg.Keys))
	for _, key := range cfg.Keys {
		cb, cbErr := cfg.Base.NewCircuitBreaker(ctx, logger, metricsProvider, circuitbreakingcfg.WithMetricAttributes(attribute.String(partitionAttributeKey, key)))
		if cbErr != nil {
			return nil, errors.Wrapf(cbErr, "providing circuit breaker for key %q", key)
		}

		breakers[key] = cb
	}

	return partitioned.NewKeyedCircuitBreaker(global, breakers), nil
}

// NewKeyedCircuitBreaker provides a KeyedCircuitBreaker from config.
func NewKeyedCircuitBreaker(ctx context.Context, cfg *Config, logger logging.Logger, metricsProvider metrics.Provider) (partitioned.KeyedCircuitBreaker, error) {
	return cfg.NewKeyedCircuitBreaker(ctx, logger, metricsProvider)
}
