package operationscfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v15/internal/scheduledjob"
	"github.com/primandproper/platform-go/v15/operations"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"

	"github.com/samber/do/v2"
)

const (
	// RecoverJobName is the name the recovery job registers under, and so its
	// lock key: stable across deploys, as a job's name has to be.
	RecoverJobName = "operations-recover"
	// ReapJobName is the name the reap job registers under.
	ReapJobName = "operations-reap"

	// DefaultRecoverInterval is how often the recovery job runs when its
	// config names no schedule. A stranded operation waits at least
	// operations.Config.RecoverAfter anyway; a minute on top of that is the
	// latency the recovery adds.
	DefaultRecoverInterval = time.Minute
	// DefaultRecoverLeaseTTL bounds one recovery pass: a read of one batch and
	// one enqueue.
	DefaultRecoverLeaseTTL = time.Minute

	// DefaultReapInterval is how often the reap job runs when its config names
	// no schedule. Retention is measured in days, so an hour is plenty, and
	// each pass is bounded by operations.Config.ReapBatchSize.
	DefaultReapInterval = time.Hour
	// DefaultReapLeaseTTL bounds one reap pass.
	DefaultReapLeaseTTL = 10 * time.Minute

	// JobsKey is the name RegisterJobs registers the two jobs under. It is
	// named rather than inferred because []jobs.Job is the application's own
	// registration — see service's scheduler — and do refuses a second
	// provider for one type.
	JobsKey = "operations.jobs"
)

// NewJobs renders the recovery and reap jobs over svc, for a jobs.Scheduler to
// run.
//
// Both belong to every deployment that runs operations: a process that dies
// between Start's insert and its enqueue leaves an operation nothing will ever
// claim until Recover re-offers it, and a table nothing reaps grows by a row per
// operation forever. So each runs unless its config says Disabled, and a
// service built from a service.Config schedules both without being asked. A
// composition root assembled by hand registers what this returns.
//
// The scheduler's lock is what makes each pass run once across a fleet rather
// than once per replica. A disabled job is left out of the result rather than
// rendered as one that never fires.
func NewJobs(ctx context.Context, cfg *Config, svc operations.Service) ([]jobs.Job, error) {
	if cfg == nil {
		return nil, operations.ErrNilConfig
	}

	if svc == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil operations service")
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating operations config")
	}

	recoverJob, err := scheduledjob.Build(&cfg.Recover, RecoverJobName, func(ctx context.Context) error {
		_, recoverErr := svc.Recover(ctx)

		return recoverErr
	})
	if err != nil {
		return nil, platformerrors.Wrap(err, "rendering the operations recovery job")
	}

	reapJob, err := scheduledjob.Build(&cfg.Reap, ReapJobName, scheduledjob.Sweep(svc.Reap))
	if err != nil {
		return nil, platformerrors.Wrap(err, "rendering the operations reap job")
	}

	return append(recoverJob, reapJob...), nil
}

// RegisterJobs registers the jobs NewJobs renders under JobsKey, over the
// registered operations.Service.
//
// service.Register calls it beside the other operations registrations, and
// service.New hands what it resolves to the scheduler with the application's
// own jobs. Recovery and reaping have no in-process fallback, so a service that
// registers these and no scheduler fails New rather than running without them.
//
// Prerequisites: context.Context, *Config, and everything RegisterService
// needs.
func RegisterJobs(i do.Injector) {
	do.ProvideNamed(i, JobsKey, func(i do.Injector) ([]jobs.Job, error) {
		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		svc, err := do.Invoke[operations.Service](i)
		if err != nil {
			return nil, err
		}

		return NewJobs(ctx, cfg, svc)
	})
}
