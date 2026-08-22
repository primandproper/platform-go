// Package idempotencycfg assembles an idempotency.Manager from environment
// configuration.
//
// It selects nothing itself. What it configures is two other seams — a
// cachecfg.Config for the record store and a distributedlockcfg.Config for the
// lock guarding the claim — and the guarantee holds only if both are chosen for
// a fleet: the memory cache is per-process, so replicas would not see each
// other's records, and the noop locker acquires unconditionally, which leaves
// replay working while quietly removing mutual exclusion.
package idempotencycfg

import (
	"context"
	"time"

	cachecfg "github.com/primandproper/platform-go/v13/cache/config"
	"github.com/primandproper/platform-go/v13/database"
	distributedlockcfg "github.com/primandproper/platform-go/v13/distributedlock/config"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles an idempotency.Manager from environment configuration.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// KeyPrefix namespaces store and lock keys.
	KeyPrefix string `env:"KEY_PREFIX" json:"keyPrefix,omitempty" yaml:"keyPrefix,omitempty"`
	// Lock configures the locker guarding the claim. It has no safe default —
	// the noop provider acquires unconditionally, which leaves replay working
	// while quietly removing mutual exclusion.
	Lock distributedlockcfg.Config `env:",init" envPrefix:"LOCK_" json:"lock,omitzero" yaml:"lock,omitempty"`

	// Cache configures the record store. Use the redis provider in
	// production: the memory provider is per-process, so replicas would not
	// see each other's records, and it holds a long TTL entirely in this
	// process's heap.
	Cache cachecfg.Config `env:",init" envPrefix:"CACHE_" json:"cache,omitzero" yaml:"cache,omitempty"`
	// TTL is how long a completed record stays replayable.
	TTL time.Duration `env:"TTL" json:"ttl,omitempty" yaml:"ttl,omitempty"`
	// InFlightTTL bounds how long a claim survives without completing. It is a
	// deadline for the guarded work, not a tuning knob: set it above the worst
	// case, since anything slower can produce a duplicate effect.
	InFlightTTL time.Duration `env:"IN_FLIGHT_TTL" json:"inFlightTTL,omitempty" yaml:"inFlightTTL,omitempty"`
	// MaxKeyLength is the longest client key accepted.
	MaxKeyLength int `env:"MAX_KEY_LENGTH" json:"maxKeyLength,omitempty" yaml:"maxKeyLength,omitempty"`
	// FailOpen runs the work when the record store cannot be read, instead of
	// refusing the request. It trades the guarantee for availability and is
	// wrong wherever a duplicate effect costs money — see
	// idempotency.StoreFailurePolicy.
	FailOpen bool `env:"FAIL_OPEN" json:"failOpen,omitempty" yaml:"failOpen,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TTL == 0 {
		cfg.TTL = idempotency.DefaultTTL
	}
	if cfg.InFlightTTL == 0 {
		cfg.InFlightTTL = idempotency.DefaultInFlightTTL
	}
	if cfg.MaxKeyLength == 0 {
		cfg.MaxKeyLength = idempotency.DefaultMaxKeyLength
	}
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = idempotency.DefaultKeyPrefix
	}
}

// ValidateWithContext validates a Config struct.
//
// The nested configs are validated through validation.By closures because ozzo
// dereferences a struct-value field before checking ValidatableWithContext,
// so it would otherwise skip them.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.TTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.InFlightTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.MaxKeyLength, validation.Min(0)),
		validation.Field(&cfg.Cache, validation.By(func(any) error {
			return cfg.Cache.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Lock, validation.By(func(any) error {
			return cfg.Lock.ValidateWithContext(ctx)
		})),
	)
}

// NewManager builds a Manager for T from configuration.
//
// T must be supplied explicitly — NewManager[Receipt](...) — because this
// constructor builds the record store itself, so nothing in the argument list
// mentions T. That single annotation is the whole cost: idempotency.Option
// carries no type parameter, so the options passed here need none.
//
// db is required only when the lock provider is postgres; pass nil otherwise.
//
// The transport adapters have their own NewManager, and callers wiring up HTTP
// or gRPC should prefer those: they apply the recordable rule for that
// transport, which this one cannot know. Reach for this when the guarded work
// is neither — a job runner, a queue consumer.
func NewManager[T any](
	ctx context.Context,
	cfg *Config,
	db database.Client,
	opts ...Option,
) (*idempotency.Manager[T], error) {
	o := newOptions(opts)

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating idempotency config")
	}

	store, err := cachecfg.NewCache[idempotency.Record[T]](ctx, &cfg.Cache,
		cachecfg.WithLogger(o.logger),
		cachecfg.WithTracerProvider(o.tracerProvider),
		cachecfg.WithMetricsProvider(o.metricsProvider))
	if err != nil {
		return nil, errors.Wrap(err, "building idempotency record store")
	}

	locker, err := distributedlockcfg.NewScopedLocker(ctx, &cfg.Lock, db,
		distributedlockcfg.WithLogger(o.logger),
		distributedlockcfg.WithTracerProvider(o.tracerProvider),
		distributedlockcfg.WithMetricsProvider(o.metricsProvider))
	if err != nil {
		return nil, errors.Wrap(err, "building idempotency locker")
	}

	policy := idempotency.FailClosed
	if cfg.FailOpen {
		policy = idempotency.FailOpen
	}

	// Caller options are appended last so they win over anything configured.
	return idempotency.NewManager(store, locker, append([]idempotency.Option{
		idempotency.WithTTL(cfg.TTL),
		idempotency.WithInFlightTTL(cfg.InFlightTTL),
		idempotency.WithMaxKeyLength(cfg.MaxKeyLength),
		idempotency.WithKeyPrefix(cfg.KeyPrefix),
		idempotency.WithStoreFailurePolicy(policy),
		idempotency.WithLogger(o.logger),
		idempotency.WithTracerProvider(o.tracerProvider),
		idempotency.WithMetricsProvider(o.metricsProvider),
	}, o.manager...)...)
}
