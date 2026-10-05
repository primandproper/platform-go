package passwordresetcfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/passwordreset"
	"github.com/primandproper/platform-go/v15/internal/scheduledjob"

	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"

	"github.com/samber/do/v2"
)

const (
	// SweepJobName is the name the sweep job registers under, and so its lock
	// key.
	SweepJobName = "password-reset-sweep"

	// DefaultSweepJobLeaseTTL bounds one scheduled sweep: a single indexed
	// DELETE.
	DefaultSweepJobLeaseTTL = time.Minute

	// JobsKey is the name RegisterJobs registers the sweep job under.
	JobsKey = "passwordreset.jobs"
)

// NewJobs renders the sweep job over sweep — a store's Sweep — for a
// jobs.Scheduler to run once across a fleet. A disabled job is left out of the
// result.
//
// It takes the method rather than the store because the Store interface has no
// Sweep: removing rows on nobody's behalf is the SQL store's machinery, not
// part of what a reset flow reads and writes. Hand it the *passwordreset.SQLStore's.
func NewJobs(ctx context.Context, cfg *Config, sweep func(context.Context) (int64, error)) ([]jobs.Job, error) {
	if cfg == nil || sweep == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating password reset config")
	}

	return scheduledjob.Build(&cfg.SweepJob, SweepJobName, scheduledjob.Sweep(sweep))
}

// sweeper is what the registered store has to be for RegisterJobs to schedule
// its sweep. *passwordreset.SQLStore is.
type sweeper interface {
	Sweep(ctx context.Context) (int64, error)
}

// RegisterJobs registers the jobs NewJobs renders under JobsKey, over the
// registered passwordreset.Store.
//
// A registered store with no Sweep is an error rather than a skipped job: a
// job that is on and quietly does nothing is the failure this exists to
// remove. Set SweepJob.Disabled for a store that reclaims its own rows.
//
// Prerequisites: context.Context, *Config, and a passwordreset.Store (see
// RegisterStore).
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

		cfg.EnsureDefaults()

		if cfg.SweepJob.Disabled {
			return nil, nil
		}

		store, err := do.Invoke[passwordreset.Store](i)
		if err != nil {
			return nil, err
		}

		swept, ok := store.(sweeper)
		if !ok {
			return nil, errors.Newf("the registered %T has no Sweep to schedule; set SweepJob.Disabled if it reclaims its own rows", store)
		}

		return NewJobs(ctx, cfg, swept.Sweep)
	})
}
