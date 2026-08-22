/*
Package jobscfg assembles the jobs package from environment configuration: a
Pool bound to a messagequeue consumer, and a Scheduler holding its periodic
executions under a distributed lock.

The two are configured independently — most services run one or the other —
so each has its own config type and builder rather than one struct with an
unused half.
*/
package jobscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	distributedlockcfg "github.com/primandproper/platform-go/v13/distributedlock/config"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/jobs"
	messagequeuecfg "github.com/primandproper/platform-go/v13/messagequeue/config"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// PoolConfig assembles a jobs.Pool from environment configuration.
type PoolConfig struct {
	_ struct{} `json:"-" yaml:"-"`

	// Queue configures the consumer the Pool reads from. It has to name one:
	// the noop consumer delivers nothing — right for tests, wrong for
	// production — so it is selected deliberately rather than fallen back to.
	Queue messagequeuecfg.Config `env:",init" envPrefix:"QUEUE_" json:"queue,omitzero" yaml:"queue,omitempty"`

	// Pool carries the worker pool's own knobs.
	Pool jobs.PoolConfig `env:",init" json:"pool,omitzero" yaml:"pool,omitempty"`
}

var _ validation.ValidatableWithContext = (*PoolConfig)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *PoolConfig) EnsureDefaults() {
	cfg.Pool.EnsureDefaults()
}

// ValidateWithContext validates a PoolConfig struct.
//
// The nested config is validated through a validation.By closure because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// it would otherwise be skipped.
func (cfg *PoolConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Pool, validation.By(func(any) error {
			return cfg.Pool.ValidateWithContext(ctx)
		})),
	)
}

// NewPool builds a Pool from configuration, including the consumer it reads
// from.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything.
func NewPool(
	ctx context.Context,
	cfg *PoolConfig,
	handler jobs.Handler,
	opts ...Option,
) (*jobs.Pool, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating jobs pool config")
	}

	provider, err := messagequeuecfg.NewConsumerProvider(ctx, &cfg.Queue,
		messagequeuecfg.WithLogger(logger),
		messagequeuecfg.WithTracerProvider(tracerProvider),
		messagequeuecfg.WithMetricsProvider(metricsProvider))
	if err != nil {
		return nil, errors.Wrap(err, "building jobs consumer provider")
	}

	var base []jobs.PoolOption
	if logger != nil {
		base = append(base, jobs.WithPoolLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, jobs.WithPoolTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, jobs.WithPoolMetricsProvider(metricsProvider))
	}

	return jobs.NewPool(ctx, &cfg.Pool, provider, handler, append(base, o.pool...)...)
}

// SchedulerConfig assembles a jobs.Scheduler from environment configuration.
type SchedulerConfig struct {
	_ struct{} `json:"-" yaml:"-"`

	// Scheduler carries the scheduler's own knobs.
	Scheduler jobs.SchedulerConfig `env:",init" json:"scheduler,omitzero" yaml:"scheduler,omitempty"`

	// Lock configures the locker that keeps a job's execution on one replica
	// per tick. It has no safe default — the noop provider acquires
	// unconditionally, which leaves every replica running every job while
	// looking configured.
	Lock distributedlockcfg.Config `env:",init" envPrefix:"LOCK_" json:"lock,omitzero" yaml:"lock,omitempty"`
}

var _ validation.ValidatableWithContext = (*SchedulerConfig)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *SchedulerConfig) EnsureDefaults() {
	cfg.Scheduler.EnsureDefaults()
}

// ValidateWithContext validates a SchedulerConfig struct.
//
// The nested config is validated through a validation.By closure because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// it would otherwise be skipped.
func (cfg *SchedulerConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Scheduler, validation.By(func(any) error {
			return cfg.Scheduler.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Lock, validation.By(func(any) error {
			return cfg.Lock.ValidateWithContext(ctx)
		})),
	)
}

// NewScheduler builds a Scheduler from configuration, including the locker it
// leases job executions under.
//
// db is required only when the lock provider is postgres; pass nil otherwise.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything.
func NewScheduler(
	ctx context.Context,
	cfg *SchedulerConfig,
	db database.Client,
	opts ...Option,
) (*jobs.Scheduler, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating jobs scheduler config")
	}

	locker, err := distributedlockcfg.NewLocker(ctx, &cfg.Lock, db,
		distributedlockcfg.WithLogger(logger),
		distributedlockcfg.WithTracerProvider(tracerProvider),
		distributedlockcfg.WithMetricsProvider(metricsProvider))
	if err != nil {
		return nil, errors.Wrap(err, "building jobs scheduler locker")
	}

	var base []jobs.SchedulerOption
	if logger != nil {
		base = append(base, jobs.WithSchedulerLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, jobs.WithSchedulerTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, jobs.WithSchedulerMetricsProvider(metricsProvider))
	}

	return jobs.NewScheduler(ctx, &cfg.Scheduler, locker, append(base, o.scheduler...)...)
}
