package signincfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/signin/refreshtokens"
	"github.com/primandproper/platform-go/v15/internal/scheduledjob"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

const (
	// RefreshTokenSweepJobName is the name the refresh token sweep job
	// registers under, and so its lock key.
	RefreshTokenSweepJobName = "signin-refresh-token-sweep"

	// DefaultSweepJobLeaseTTL bounds one scheduled sweep: a single indexed
	// DELETE.
	DefaultSweepJobLeaseTTL = time.Minute

	// JobsKey is the name RegisterJobs registers the sweep job under.
	JobsKey = "signin.jobs"
)

// NewJobs renders the refresh token sweep job over sweep — the refresh token
// store's Sweep — for a jobs.Scheduler to run once across a fleet. A disabled
// job is left out of the result.
//
// Under rotation the refresh token table grows by a row per refresh rather than
// per sign-in, which is why it is the table here whose sweep is scheduled.
func NewJobs(ctx context.Context, cfg *Config, sweep func(context.Context) (int64, error)) ([]jobs.Job, error) {
	if cfg == nil || sweep == nil {
		return nil, errors.ErrNilInputParameter
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating sign-in config")
	}

	return scheduledjob.Build(&cfg.RefreshTokens.SweepJob, RefreshTokenSweepJobName, scheduledjob.Sweep(sweep))
}

// RegisterJobs registers the jobs NewJobs renders under JobsKey.
//
// The refresh token store is built inside the *signin.Service RegisterService
// provides and is not registered on its own, so the job sweeps through a store
// of its own over the same table: a handle with no sweeper started, holding
// nothing but the client and the table name. A sweep is a DELETE decided by the
// rows themselves, so which handle issues it changes nothing about what goes.
//
// Prerequisites: context.Context, *Config, and database.Client.
func RegisterJobs(i do.Injector) {
	do.ProvideNamed(i, JobsKey, func(i do.Injector) ([]jobs.Job, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		ctx, err := do.Invoke[context.Context](i)
		if err != nil {
			return nil, err
		}

		cfg, err := do.Invoke[*Config](i)
		if err != nil {
			return nil, err
		}

		if err = cfg.ValidateWithContext(ctx); err != nil {
			return nil, errors.Wrap(err, "validating sign-in config")
		}

		if cfg.RefreshTokens.SweepJob.Disabled {
			return nil, nil
		}

		client, err := do.Invoke[database.Client](i)
		if err != nil {
			return nil, err
		}

		options := newOptions([]Option{WithPillars(pillars)})

		store, err := refreshtokens.NewSQLStore(&refreshtokens.Config{TablePrefix: cfg.RefreshTokens.TablePrefix}, client,
			refreshtokens.WithLogger(options.logger),
			refreshtokens.WithTracerProvider(options.tracerProvider),
			refreshtokens.WithMetricsProvider(options.metricsProvider),
		)
		if err != nil {
			return nil, errors.Wrap(err, "building the refresh token store the sweep job runs through")
		}

		return NewJobs(ctx, cfg, store.Sweep)
	})
}
