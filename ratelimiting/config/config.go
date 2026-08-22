// Package ratelimitingcfg selects and builds a rate limiter from configuration:
// the in-process memory limiter, the Redis-backed one, or noop.
//
// The rate and burst are shared across providers, but they mean different
// things: the memory limiter enforces them per replica, so a fleet of N allows
// N times what the config says, while the Redis limiter enforces them once for
// the fleet. MaxLimiters bounds how many per-key limiters the memory provider
// retains and is ignored by the others, which keep no per-key state here.
package ratelimitingcfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/ratelimiting"
	"github.com/primandproper/platform-go/v13/ratelimiting/noop"
	redisrl "github.com/primandproper/platform-go/v13/ratelimiting/redis"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderMemory selects the in-process limiter.
	ProviderMemory = "memory"
	// ProviderNoop selects a limiter that allows everything. It must be chosen
	// deliberately — an unset or unrecognized provider is an error, because a
	// limiter that silently stops limiting is indistinguishable from one that is
	// simply never hit.
	ProviderNoop = "noop"
	// ProviderRedis selects the redis-backed limiter.
	ProviderRedis = "redis"

	defaultRequestsPerSec = 10.0
	defaultBurstSize      = 20
)

// providers are every provider this package implements. The dispatch switch and
// ValidateWithContext both read it, so they cannot drift apart.
var providers = []string{ProviderMemory, ProviderNoop, ProviderRedis}

// Config configures rate limiting.
type Config struct {
	Provider       string         `env:"PROVIDER"         json:"provider,omitempty"          yaml:"provider,omitempty"`
	Redis          redisrl.Config `env:",init"            envPrefix:"REDIS_"                 json:"redis,omitzero"              yaml:"redis,omitempty"`
	RequestsPerSec float64        `env:"REQUESTS_PER_SEC" json:"requestsPerSecond,omitempty" yaml:"requestsPerSecond,omitempty"`
	BurstSize      int            `env:"BURST_SIZE"       json:"burstSize,omitempty"         yaml:"burstSize,omitempty"`
	// MaxLimiters caps how many per-key limiters the memory provider holds at
	// once. Zero takes ratelimiting.DefaultMaxLimiters; negative removes the
	// bound, which is only safe where the key space is known to be small. It is
	// ignored by the other providers, which keep no per-key state in this
	// process.
	MaxLimiters int `env:"MAX_LIMITERS" json:"maxLimiters,omitempty" yaml:"maxLimiters,omitempty"`
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
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			if !slices.Contains(providers, cfgnorm.Provider(cfg.Provider)) {
				return errors.Wrapf(errors.ErrUnknownProvider, "rate limiter provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.RequestsPerSec, validation.Min(0.0)),
		validation.Field(&cfg.BurstSize, validation.Min(0)),
	)
}

// NewRateLimiter returns a RateLimiter from config.
//
// Defaults are applied before validation, so an unset RequestsPerSec is the
// documented default rather than a validation failure; a negative one is still
// rejected, because a negative rate rejects every request.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *redis.RateLimiter into a
// non-nil ratelimiting.RateLimiter on the error path, and a caller testing the result against
// nil would find a limiter that panics on first use.
func NewRateLimiter(
	ctx context.Context,
	cfg *Config,
	opts ...Option,
) (ratelimiting.RateLimiter, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	// Checked before the rest of the config so an unrecognized provider reports
	// ErrUnknownProvider rather than a downstream consequence of it.
	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "rate limiter provider")
	if err != nil {
		return nil, err
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating rate limiter config")
	}

	switch provider {
	case ProviderNoop:
		return noop.NewRateLimiter(), nil
	case ProviderMemory:
		memoryOpts := []ratelimiting.Option{
			ratelimiting.WithLogger(logger),
			ratelimiting.WithTracerProvider(tracerProvider),
			ratelimiting.WithMetricsProvider(metricsProvider),
		}

		// Passed only when the config says something, so the default lives in
		// one place — the package that has to honor it — rather than being
		// copied into EnsureDefaults where it would drift.
		if cfg.MaxLimiters != 0 {
			memoryOpts = append(memoryOpts, ratelimiting.WithMaxLimiters(cfg.MaxLimiters))
		}

		// The in-memory limiter's eviction sweep runs on a context of its own,
		// deliberately: this one bounds construction, and a sweep tied to it
		// would stop reclaiming keys the moment startup finished. What ends the
		// sweep is Close.
		//
		//nolint:contextcheck // the sweep outlives this ctx by design; see above.
		l, limiterErr := ratelimiting.NewInMemoryRateLimiter(cfg.RequestsPerSec, cfg.BurstSize, memoryOpts...)
		if limiterErr != nil {
			return nil, limiterErr
		}

		return l, nil
	case ProviderRedis:
		l, limiterErr := redisrl.NewRedisRateLimiter(ctx, cfg.Redis, cfg.RequestsPerSec, cfg.BurstSize,
			redisrl.WithLogger(logger),
			redisrl.WithTracerProvider(tracerProvider),
			redisrl.WithMetricsProvider(metricsProvider))
		if limiterErr != nil {
			return nil, limiterErr
		}

		return l, nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "rate limiter provider %q", cfg.Provider)
	}
}
