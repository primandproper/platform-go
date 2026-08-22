// Package cachecfg selects and builds a cache.Cache[T] from configuration:
// either the in-process memory cache or Redis.
//
// There is deliberately no default provider. Whether a cache lives in this
// process or in Redis is a fact about a deployment rather than something a
// library can pick, so an unset Provider fails validation instead of quietly
// becoming a memory cache. Several fields — MaxEntries, EvictionPolicy,
// JanitorInterval — are read only by the memory provider, which is the only one
// holding entries in this process's heap.
package cachecfg

import (
	"context"
	"slices"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/cache/redis"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"

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
		Redis    *redis.Config `env:",init"    envPrefix:"REDIS_"        json:"redis,omitempty"    yaml:"redis,omitempty"`
		Provider string        `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
		// EvictionPolicy names which entry the memory provider drops once
		// MaxEntries is reached: "least_recently_used" (alias "lru") or
		// "oldest_written" (alias "fifo"). It is read only when MaxEntries is
		// positive, so the default here costs an unbounded cache nothing.
		EvictionPolicy string                    `env:"EVICTION_POLICY" envDefault:"least_recently_used" json:"evictionPolicy,omitempty"      yaml:"evictionPolicy,omitempty"`
		CircuitBreaker circuitbreakingcfg.Config `env:",init"           envPrefix:"CIRCUIT_BREAKING_"    json:"circuitBreakerConfig,omitzero" yaml:"circuitBreakerConfig,omitempty"`
		// Expiry is the default expiry for writes that don't specify one via
		// cache.WithExpiry; a non-positive value means entries never expire by
		// default.
		Expiry time.Duration `env:"EXPIRY" envDefault:"1h" json:"expiry,omitempty" yaml:"expiry,omitempty"`
		// JanitorInterval is how often the memory provider sweeps expired
		// entries. It is ignored by every other provider, which expire entries
		// in the backing store rather than in this process. A non-positive
		// value disables the sweep, leaving the memory provider's lazy
		// eviction as the only reclaim path — see memory.WithJanitor for why
		// that is rarely what a long-lived cache wants.
		JanitorInterval time.Duration `env:"JANITOR_INTERVAL" envDefault:"5m" json:"janitorInterval,omitempty" yaml:"janitorInterval,omitempty"`
		// MaxEntries bounds how many entries the memory provider holds,
		// dropping one per EvictionPolicy whenever a write would exceed it. It
		// is ignored by every other provider, which bound their own storage.
		// A non-positive value leaves the cache bounded only by expiry — see
		// memory.WithMaxEntries for when that is not enough.
		MaxEntries int `env:"MAX_ENTRIES" json:"maxEntries,omitempty" yaml:"maxEntries,omitempty"`
	}
)

// providers are every provider this package implements. Validation and NewCache
// both read it. The empty string is absent deliberately: a cache is either in
// this process or in Redis, and which one is not a question the library can
// answer for a deployment.
var providers = []string{ProviderMemory, ProviderRedis}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
//
// The sub-config for a provider that was not selected is skipped rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves every sub-config non-nil. A
// validation.When guard alone stops the Required rule and nothing else, so
// Redis' own rules were enforced and the memory provider could not load.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	provider := cfgnorm.Provider(cfg.Provider)

	return validation.ValidateStructWithContext(ctx, cfg,
		// Required as well as known: an unset provider was accepted here and
		// then refused by NewCache, so the one config that could not work was
		// also the one validation had nothing to say about.
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "Redis" and " redis " while NewCache built them.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "cache provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.Redis, validation.Skip.When(provider != ProviderRedis), validation.Required),
		validation.Field(&cfg.EvictionPolicy,
			validation.Skip.When(provider != ProviderMemory || cfg.MaxEntries <= 0),
			validation.By(validEvictionPolicy)),
	)
}

// validEvictionPolicy reports whether the configured policy names one the
// memory provider implements, so a typo fails validation rather than
// construction. The check is skipped for a cache that will not be bounded,
// which is why the field's default costs an unbounded cache nothing.
func validEvictionPolicy(value any) error {
	name, ok := value.(string)
	if !ok {
		return errors.Newf("expected a string eviction policy, got %T", value)
	}

	_, err := memory.ParseEvictionPolicy(name)

	return err
}

// NewCache provides a Cache.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil, rather than returned straight from the constructor. The
// provider constructors return their own concrete types, so `return
// memory.NewInMemoryCache[T](...)` would convert a nil *memory.Cache[T] into a
// non-nil cache.Cache[T] on the error path, and a caller testing the returned
// interface against nil would find a cache that panics on first use.
func NewCache[T any](ctx context.Context, cfg *Config, opts ...Option) (cache.Cache[T], error) {
	o := newOptions(opts)

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "cache provider")
	if err != nil {
		return nil, err
	}

	// Resolved before the config's own rules, for the reason the provider is:
	// ozzo's validation.Errors is a map with no Unwrap, so a caller matching
	// memory.ErrUnknownEvictionPolicy would stop finding it the moment
	// validation reported the same typo first. Resolved only for a cache that
	// will be bounded, so an unbounded one is never failed by a policy it would
	// not have consulted.
	var policy memory.EvictionPolicy
	if provider == ProviderMemory && cfg.MaxEntries > 0 {
		if policy, err = memory.ParseEvictionPolicy(cfg.EvictionPolicy); err != nil {
			return nil, errors.Wrap(err, "resolving cache eviction policy")
		}
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating cache config")
	}

	switch provider {
	case ProviderMemory:
		// The janitor is bound to the caller's context because cache.Cache has
		// no Close: the sweep stops when whatever scope owns this cache does.
		memoryOpts := []memory.Option{
			memory.WithLogger(o.logger),
			memory.WithTracerProvider(o.tracerProvider),
			memory.WithMetricsProvider(o.metricsProvider),
			memory.WithJanitor(ctx, cfg.JanitorInterval),
		}

		if cfg.MaxEntries > 0 {
			memoryOpts = append(memoryOpts, memory.WithMaxEntries(cfg.MaxEntries, policy))
		}

		c, cacheErr := memory.NewInMemoryCache[T](cfg.Expiry, memoryOpts...)
		if cacheErr != nil {
			return nil, cacheErr
		}

		return c, nil
	case ProviderRedis:
		cb, breakerErr := cfg.CircuitBreaker.NewCircuitBreaker(ctx,
			circuitbreakingcfg.WithLogger(o.logger),
			circuitbreakingcfg.WithMetricsProvider(o.metricsProvider))
		if breakerErr != nil {
			return nil, errors.Wrap(breakerErr, "initializing cache circuit breaker")
		}
		c, cacheErr := redis.NewRedisCache[T](cfg.Redis, cfg.Expiry, cb,
			redis.WithLogger(o.logger),
			redis.WithTracerProvider(o.tracerProvider),
			redis.WithMetricsProvider(o.metricsProvider))
		if cacheErr != nil {
			return nil, cacheErr
		}

		return c, nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "cache provider %q", cfg.Provider)
	}
}
