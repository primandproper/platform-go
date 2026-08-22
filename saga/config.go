package saga

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultPollInterval is how often the Worker looks for instances to
	// advance.
	//
	// A second, not the ten this module's other durable workers use. A saga
	// coordinates a process somebody is waiting on — an order, a booking — and
	// the poll interval is the floor on how long a step's delay costs. It is a
	// cheap indexed query against a partial index over the in-flight set.
	DefaultPollInterval = time.Second

	// DefaultBatchSize is how many instances one Worker cycle claims.
	DefaultBatchSize = 10

	// DefaultConcurrency is how many claimed instances a Worker advances at
	// once.
	DefaultConcurrency = 4

	// DefaultStepTimeout bounds one Do or one Undo. It exists so that a step
	// calling a third party that never answers costs its own attempt rather
	// than the lease.
	DefaultStepTimeout = 30 * time.Second

	// DefaultAdvanceTimeout bounds one pass over an instance.
	//
	// A pass runs as many steps as it can rather than one per poll, because a
	// five-step saga that took five poll intervals to finish would make the
	// poll interval the latency of every saga in the system. This is the budget
	// that pass gets before it hands the instance back — half the lease, so a
	// pass that overruns still has a margin before another worker may claim.
	DefaultAdvanceTimeout = 2 * time.Minute

	// DefaultLeaseDuration is how long a claimed instance stays leased. It must
	// exceed AdvanceTimeout, or a second worker starts advancing an instance
	// the first is still stepping through.
	DefaultLeaseDuration = 5 * time.Minute

	// DefaultLockTTL bounds the per-instance distributed lock. Like the lease,
	// it must exceed AdvanceTimeout — a lock that expires mid-pass is not a
	// lock.
	DefaultLockTTL = 5 * time.Minute

	// DefaultLockKeyPrefix namespaces the per-instance lock keys, so a saga
	// cannot collide with an unrelated entry in a locker shared with something
	// else.
	DefaultLockKeyPrefix = "saga:instance:"

	// DefaultIdempotencyKeyPrefix namespaces the per-step idempotency keys, for
	// the same reason.
	DefaultIdempotencyKeyPrefix = "saga:"

	// DefaultCompensationAttempts is how many times a compensation is tried
	// before the instance is marked stuck.
	//
	// Higher than the forward budget, deliberately. Giving up on a Do costs a
	// compensation; giving up on an Undo costs a person's evening. The extra
	// attempts are the cheapest thing in this package.
	DefaultCompensationAttempts uint = 10
)

// WorkerConfig configures the loop that advances instances.
type WorkerConfig struct {
	// LockKeyPrefix namespaces the per-instance lock keys. Defaults to
	// DefaultLockKeyPrefix.
	LockKeyPrefix string `env:"LOCK_KEY_PREFIX" json:"lockKeyPrefix,omitempty" yaml:"lockKeyPrefix,omitempty"`

	// IdempotencyKeyPrefix namespaces the per-step idempotency keys. Defaults
	// to DefaultIdempotencyKeyPrefix.
	//
	// Changing it after instances are in flight re-arms every step that has
	// already run: the keys the resumed instance computes will not be the ones
	// its earlier attempts recorded. Treat it as part of the schema.
	IdempotencyKeyPrefix string `env:"IDEMPOTENCY_KEY_PREFIX" json:"idempotencyKeyPrefix,omitempty" yaml:"idempotencyKeyPrefix,omitempty"`

	// Backoff schedules the retry of a step that failed, and its MaxAttempts is
	// the forward budget: a Do that fails this many times begins compensation.
	Backoff retrycfg.Config `env:",init" envPrefix:"BACKOFF_" json:"backoff,omitzero" yaml:"backoff,omitempty"`

	// CompensationBackoff schedules the retry of a compensation. Its
	// MaxAttempts is the budget past which the instance is marked stuck.
	CompensationBackoff retrycfg.Config `env:",init" envPrefix:"COMPENSATION_BACKOFF_" json:"compensationBackoff,omitzero" yaml:"compensationBackoff,omitempty"`

	// PollInterval is how often the Worker looks for instances to advance.
	PollInterval time.Duration `env:"POLL_INTERVAL" json:"pollInterval,omitempty" yaml:"pollInterval,omitempty"`

	// LeaseDuration is how long a claimed instance stays leased. It must exceed
	// AdvanceTimeout — see ValidateWithContext.
	LeaseDuration time.Duration `env:"LEASE_DURATION" json:"leaseDuration,omitempty" yaml:"leaseDuration,omitempty"`

	// LockTTL bounds the per-instance distributed lock. It must exceed
	// AdvanceTimeout for the same reason the lease must.
	LockTTL time.Duration `env:"LOCK_TTL" json:"lockTTL,omitempty" yaml:"lockTTL,omitempty"`

	// AdvanceTimeout bounds one pass over one instance.
	AdvanceTimeout time.Duration `env:"ADVANCE_TIMEOUT" json:"advanceTimeout,omitempty" yaml:"advanceTimeout,omitempty"`

	// StepTimeout bounds one Do or one Undo.
	StepTimeout time.Duration `env:"STEP_TIMEOUT" json:"stepTimeout,omitempty" yaml:"stepTimeout,omitempty"`

	// BatchSize is how many instances one cycle claims.
	BatchSize int `env:"BATCH_SIZE" json:"batchSize,omitempty" yaml:"batchSize,omitempty"`

	// Concurrency is how many claimed instances are advanced at once.
	Concurrency int `env:"CONCURRENCY" json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
}

var _ validation.ValidatableWithContext = (*WorkerConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *WorkerConfig) EnsureDefaults() {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = DefaultLeaseDuration
	}
	if cfg.LockTTL <= 0 {
		cfg.LockTTL = DefaultLockTTL
	}
	if cfg.AdvanceTimeout <= 0 {
		cfg.AdvanceTimeout = DefaultAdvanceTimeout
	}
	if cfg.StepTimeout <= 0 {
		cfg.StepTimeout = DefaultStepTimeout
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.LockKeyPrefix == "" {
		cfg.LockKeyPrefix = DefaultLockKeyPrefix
	}
	if cfg.IdempotencyKeyPrefix == "" {
		cfg.IdempotencyKeyPrefix = DefaultIdempotencyKeyPrefix
	}

	cfg.Backoff.EnsureDefaults()

	if cfg.CompensationBackoff.MaxAttempts == 0 {
		cfg.CompensationBackoff.MaxAttempts = DefaultCompensationAttempts
	}

	cfg.CompensationBackoff.EnsureDefaults()
}

// ValidateWithContext validates a WorkerConfig.
func (cfg *WorkerConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.PollInterval, validation.Required),
		validation.Field(&cfg.BatchSize, validation.Required, validation.Min(1)),
		validation.Field(&cfg.Concurrency, validation.Required, validation.Min(1)),
		validation.Field(&cfg.StepTimeout, validation.Required),
		validation.Field(&cfg.AdvanceTimeout, validation.Required, validation.By(func(any) error {
			// A step that cannot fit inside a pass can never complete: the pass
			// deadline would cut it off every time, and the instance would be
			// retried forever until its budget ran out and it compensated work
			// that had in fact been done.
			if cfg.AdvanceTimeout < cfg.StepTimeout {
				return platformerrors.Newf(
					"saga advance timeout %s must be at least the step timeout %s",
					cfg.AdvanceTimeout, cfg.StepTimeout,
				)
			}

			return nil
		})),
		validation.Field(&cfg.LeaseDuration, validation.Required, validation.By(func(any) error {
			// A lease that expires while the pass it covers is still running is
			// not a lease. Two workers stepping through the same saga means two
			// Do calls racing, and the idempotency key narrows that window but
			// does not close it.
			//
			// The bound is the pass budget plus one step, not the pass budget
			// alone: a pass stops starting steps once its budget is spent, so
			// the last step it did start can run for a further StepTimeout.
			if bound := cfg.AdvanceTimeout + cfg.StepTimeout; cfg.LeaseDuration <= bound {
				return platformerrors.Newf(
					"saga lease duration %s must exceed the advance timeout plus the step timeout (%s)",
					cfg.LeaseDuration, bound,
				)
			}

			return nil
		})),
		validation.Field(&cfg.LockTTL, validation.Required, validation.By(func(any) error {
			// Same bound, same reason: a lock that expires mid-pass is not a
			// lock, and this one is the last thing standing between two
			// overlapping leases and two charges.
			if bound := cfg.AdvanceTimeout + cfg.StepTimeout; cfg.LockTTL <= bound {
				return platformerrors.Newf(
					"saga lock TTL %s must exceed the advance timeout plus the step timeout (%s)",
					cfg.LockTTL, bound,
				)
			}

			return nil
		})),
		validation.Field(&cfg.LockKeyPrefix, validation.Required),
		validation.Field(&cfg.IdempotencyKeyPrefix, validation.Required),
		validation.Field(&cfg.Backoff),
		validation.Field(&cfg.CompensationBackoff),
	)
}

// budgetFor returns the attempt budget and backoff schedule for a phase.
func (cfg *WorkerConfig) budgetFor(phase string) retrycfg.Config {
	if phase == phaseUndo {
		return cfg.CompensationBackoff
	}

	return cfg.Backoff
}
