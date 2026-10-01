package sagacfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/internal/scheduledjob"
	"github.com/primandproper/platform-go/v14/retention"
	"github.com/primandproper/platform-go/v14/saga"

	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/samber/do/v2"
)

const (
	// CompletedRetentionPolicyName is the retention policy that removes
	// completed instances. It is the policy's identity in the audit record and
	// in every metric attribute, so it is a constant rather than a string each
	// deployment picks.
	CompletedRetentionPolicyName = "saga-completed"
	// CompensatedRetentionPolicyName is the retention policy that removes
	// compensated instances.
	CompensatedRetentionPolicyName = "saga-compensated"

	// RetentionJobName is the name the retention job registers under, and so
	// its lock key. It is not retention.DefaultSweepJobName: this sweep runs
	// saga's two policies on a sweeper of its own, and a deployment that also
	// schedules its own retention.Sweeper would otherwise have two jobs under
	// one name.
	RetentionJobName = "saga-retention"

	// DefaultRetentionInterval is how often the retention job runs when its
	// config names no schedule.
	DefaultRetentionInterval = time.Hour
	// DefaultRetentionLeaseTTL bounds one retention pass. A retention.Sweeper
	// at its defaults can spend over a minute and a half pausing between
	// batches alone, and the lease is not renewed while it runs.
	DefaultRetentionLeaseTTL = 30 * time.Minute

	// JobsKey is the name RegisterJobs registers the retention job under.
	JobsKey = "saga.jobs"

	completedBasis = "a completed saga has nothing left to say beyond the lifecycle events it " +
		"already published; it is kept for the support question that arrives late"
	compensatedBasis = "a compensated saga is the one somebody debugs, so it is kept longer " +
		"than a completed one, and past that it is state nobody will unwind again"
)

// RetentionConfig is how long finished instances are kept, and the job that
// removes them.
//
// Only the two terminal statuses a saga can finish in are removed: a running,
// compensating or stuck instance is never swept, whatever its age.
// saga.RetentionTarget refuses any other status at construction.
type RetentionConfig struct {
	_ struct{} `json:"-" yaml:"-"`

	// Job is when the pass runs. It runs unless Disabled, every
	// DefaultRetentionInterval unless it names its own schedule.
	Job jobscfg.JobConfig `env:",init" envPrefix:"JOB_" json:"job,omitzero" yaml:"job,omitempty"`

	// Sweeper bounds a pass: how many rows a batch removes and how many
	// batches it spends.
	Sweeper retention.SweeperConfig `env:",init" envPrefix:"SWEEPER_" json:"sweeper,omitzero" yaml:"sweeper,omitempty"`

	// Completed is how long a completed instance is kept after it completed.
	// Defaults to saga.DefaultCompletedRetention.
	Completed time.Duration `env:"COMPLETED" json:"completed,omitempty" yaml:"completed,omitempty"`

	// Compensated is how long a compensated instance is kept after it
	// finished unwinding. Defaults to saga.DefaultCompensatedRetention.
	Compensated time.Duration `env:"COMPENSATED" json:"compensated,omitempty" yaml:"compensated,omitempty"`
}

var _ validation.ValidatableWithContext = (*RetentionConfig)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *RetentionConfig) EnsureDefaults() {
	if cfg.Completed <= 0 {
		cfg.Completed = saga.DefaultCompletedRetention
	}

	if cfg.Compensated <= 0 {
		cfg.Compensated = saga.DefaultCompensatedRetention
	}

	cfg.Sweeper.EnsureDefaults()
	scheduledjob.EnsureDefaults(&cfg.Job, DefaultRetentionInterval, DefaultRetentionLeaseTTL)
}

// ValidateWithContext validates a RetentionConfig.
func (cfg *RetentionConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		// An hour, not a second: a window measured in seconds is a unit
		// mistake, and against these rows it deletes a saga the moment it
		// finishes.
		validation.Field(&cfg.Completed, validation.Required, validation.Min(time.Hour)),
		validation.Field(&cfg.Compensated, validation.Required, validation.Min(time.Hour)),
		validation.Field(&cfg.Sweeper, validation.By(func(any) error {
			return cfg.Sweeper.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Job, validation.By(func(any) error {
			return scheduledjob.Validate(ctx, &cfg.Job)
		})),
	)
}

// NewRetentionPolicies returns the two retention policies saga's table is
// swept by: completed instances past Retention.Completed, and compensated ones
// past Retention.Compensated.
//
// They are policies rather than a sweep of their own so a deployment that runs
// its own retention.Sweeper can append them to its set instead of scheduling
// NewJobs. It should do one or the other: both is two sweeps of one table.
func NewRetentionPolicies(cfg *Config) ([]retention.Policy, error) {
	if cfg == nil {
		return nil, errors.New("nil saga config provided")
	}

	cfg.EnsureDefaults()

	return []retention.Policy{
		{
			Name:   CompletedRetentionPolicyName,
			Target: saga.RetentionTarget{TablePrefix: cfg.TablePrefix, Status: saga.StatusCompleted},
			Basis:  completedBasis,
			Scope:  tenancy.Global(),
			Age:    cfg.Retention.Completed,
		},
		{
			Name:   CompensatedRetentionPolicyName,
			Target: saga.RetentionTarget{TablePrefix: cfg.TablePrefix, Status: saga.StatusCompensated},
			Basis:  compensatedBasis,
			Scope:  tenancy.Global(),
			Age:    cfg.Retention.Compensated,
		},
	}, nil
}

// NewJobs renders the retention job: a retention.Sweeper over
// NewRetentionPolicies, scheduled under RetentionJobName.
//
// Nothing else removes a finished saga, so the job runs unless
// Retention.Job.Disabled, and a service built from a service.Config schedules
// it without being asked. A disabled job is left out of the result. The
// sweeper's own options — the audit recorder that accounts for each pass among
// them — arrive through WithRetentionSweeperOptions.
func NewJobs(ctx context.Context, cfg *Config, client database.Client, opts ...Option) ([]jobs.Job, error) {
	if cfg == nil {
		return nil, errors.New("nil saga config provided")
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating saga config")
	}

	if cfg.Retention.Job.Disabled {
		return nil, nil
	}

	policies, err := NewRetentionPolicies(cfg)
	if err != nil {
		return nil, err
	}

	o := newOptions(opts)

	sweeper, err := retention.NewSweeper(ctx, &cfg.Retention.Sweeper, client, policies,
		append([]retention.SweeperOption{
			retention.WithSweeperLogger(o.logger),
			retention.WithSweeperTracerProvider(o.tracerProvider),
			retention.WithSweeperMetricsProvider(o.metricsProvider),
		}, o.retention...)...)
	if err != nil {
		return nil, errors.Wrap(err, "building the saga retention sweeper")
	}

	return scheduledjob.Build(&cfg.Retention.Job, RetentionJobName, func(ctx context.Context) error {
		_, sweepErr := sweeper.Sweep(ctx)

		return sweepErr
	})
}

// RegisterJobs registers the jobs NewJobs renders under JobsKey.
//
// The audit.Recorder is resolved optionally, as retentioncfg.RegisterSweeper
// resolves it: a container that registers one gets each pass accounted for in
// the audit log, and one that does not gets a pass that still runs.
//
// Prerequisites: context.Context, *Config, and database.Client.
func RegisterJobs(i do.Injector) {
	do.ProvideNamed(i, JobsKey, func(i do.Injector) ([]jobs.Job, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		recorder, err := injection.InvokeOptional[audit.Recorder](i)
		if err != nil {
			return nil, err
		}

		opts := []Option{WithPillars(pillars)}
		if recorder != nil {
			opts = append(opts, WithRetentionSweeperOptions(retention.WithSweeperAuditRecorder(recorder)))
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		return NewJobs(ctx, cfg, client, opts...)
	})
}
