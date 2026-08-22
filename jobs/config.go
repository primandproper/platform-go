package jobs

import (
	"context"
	"time"

	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// SchedulerConfig configures a Scheduler.
type SchedulerConfig struct {
	// LockKeyPrefix namespaces every lock key the Scheduler takes.
	LockKeyPrefix string `env:"LOCK_KEY_PREFIX" json:"lockKeyPrefix,omitempty" yaml:"lockKeyPrefix,omitempty"`
	// Timezone is the IANA name of the zone that cron schedules are read in
	// when they did not settle the question themselves — "America/Chicago". It
	// is what a service whose jobs all belong to one calendar sets once,
	// instead of repeating a CRON_TZ prefix on every expression.
	//
	// A schedule built by CronIn, or one whose spec carries its own CRON_TZ,
	// ignores this: both are more specific. Empty means DefaultTimezone.
	//
	// It is resolved by NewScheduler, so a name the runtime cannot load fails
	// construction rather than the first fire. Anything but UTC needs the
	// zoneinfo database in the image.
	Timezone string `env:"TIMEZONE" json:"timezone,omitempty" yaml:"timezone,omitempty"`
	// DefaultLeaseTTL is the lease length for jobs that do not set their own.
	DefaultLeaseTTL time.Duration `env:"DEFAULT_LEASE_TTL" json:"defaultLeaseTTL,omitempty" yaml:"defaultLeaseTTL,omitempty"`
	// DefaultTimeout bounds one execution for jobs that do not set their own.
	// Zero means no timeout.
	DefaultTimeout time.Duration `env:"DEFAULT_TIMEOUT" json:"defaultTimeout,omitempty" yaml:"defaultTimeout,omitempty"`
}

var _ validation.ValidatableWithContext = (*SchedulerConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *SchedulerConfig) EnsureDefaults() {
	if cfg.LockKeyPrefix == "" {
		cfg.LockKeyPrefix = DefaultLockKeyPrefix
	}

	if cfg.DefaultLeaseTTL <= 0 {
		cfg.DefaultLeaseTTL = DefaultLeaseTTL
	}

	if cfg.Timezone == "" {
		cfg.Timezone = DefaultTimezone
	}
}

// ValidateWithContext validates a SchedulerConfig.
func (cfg *SchedulerConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.DefaultLeaseTTL, validation.Required, validation.Min(time.Second)),
		validation.Field(&cfg.DefaultTimeout, validation.Min(time.Duration(0))),
	)
}

// PoolConfig configures a Pool.
type PoolConfig struct {
	// Topic is the queue topic to consume.
	Topic string `env:"TOPIC" json:"topic,omitempty" yaml:"topic,omitempty"`
	// Retry drives per-message retries. MaxAttempts is how many times the
	// handler runs before the message is dead-lettered, so MaxAttempts of 1
	// means no retry at all.
	Retry retrycfg.Config `envPrefix:"RETRY_" json:"retry,omitzero" yaml:"retry,omitempty"`
	// Concurrency is how many messages the Pool handles at once. It is also the
	// bound on read-ahead: the Pool holds at most this many messages in memory,
	// because a consumed message is handed directly to a free worker and the
	// consumer blocks when there is none.
	Concurrency int `env:"CONCURRENCY" json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
	// HandlerTimeout bounds one attempt. Zero — the default — means no timeout,
	// in which case a handler that neither returns nor honors its context
	// occupies a worker permanently and will hold up Close.
	HandlerTimeout time.Duration `env:"HANDLER_TIMEOUT" json:"handlerTimeout,omitempty" yaml:"handlerTimeout,omitempty"`
}

var _ validation.ValidatableWithContext = (*PoolConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *PoolConfig) EnsureDefaults() {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}

	cfg.Retry.EnsureDefaults()
}

// ValidateWithContext validates a PoolConfig.
func (cfg *PoolConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Topic, validation.Required),
		validation.Field(&cfg.Concurrency, validation.Required, validation.Min(1)),
		validation.Field(&cfg.HandlerTimeout, validation.Min(time.Duration(0))),
		validation.Field(&cfg.Retry, validation.By(func(any) error { return cfg.Retry.ValidateWithContext(ctx) })),
	)
}
