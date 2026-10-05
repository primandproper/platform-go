package sessionscfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v15/internal/scheduledjob"
	sessionsdatabase "github.com/primandproper/platform-go/v15/sessions/database"

	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
)

const (
	// SweepJobName is the name the sweep job registers under, and so its lock
	// key.
	SweepJobName = "sessions-sweep"

	// DefaultSweepJobLeaseTTL bounds one scheduled sweep: a single indexed
	// DELETE.
	DefaultSweepJobLeaseTTL = time.Minute

	// JobsKey is the name RegisterJobs registers the sweep job under.
	JobsKey = "sessions.jobs"
)

// NewJobs renders the sweep job under the database provider, for a
// jobs.Scheduler to run once across a fleet. It renders nothing under the cache
// provider, which reclaims its own entries, or when the job is disabled.
//
// It builds the backend it sweeps through rather than taking the store's: a
// sessions.Store[T] is deliberately not where a sweep lives, and the backend
// NewStore built is inside it. The handle here starts no sweeper and holds
// nothing but the client and the table name. Its payload type is struct{}
// because a sweep deletes rows by their deadlines and never decodes one, so
// which T a handle was built for changes nothing about what goes.
//
// db is required under the database provider; pass nil otherwise.
func NewJobs(ctx context.Context, cfg *Config, db database.Client, opts ...Option) ([]jobs.Job, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating sessions config")
	}

	if cfg.provider() != ProviderDatabase || cfg.SweepJob.Disabled {
		return nil, nil
	}

	o := newOptions(opts)

	backend, err := sessionsdatabase.NewBackend[struct{}](&cfg.Database, db,
		sessionsdatabase.WithLogger(o.logger),
		sessionsdatabase.WithTracerProvider(o.tracerProvider),
		sessionsdatabase.WithMetricsProvider(o.metricsProvider),
	)
	if err != nil {
		return nil, errors.Wrap(err, "building the session backend the sweep job runs through")
	}

	return scheduledjob.Build(&cfg.SweepJob, SweepJobName, scheduledjob.Sweep(backend.Sweep))
}

// RegisterJobs registers the jobs NewJobs renders under JobsKey. A service built
// from a service.Config schedules them when a container registers them, beside
// the ones it registers itself.
//
// Prerequisites: context.Context and *Config. A database.Client is resolved only
// if one is registered, as RegisterStore resolves it.
func RegisterJobs(i do.Injector) {
	do.ProvideNamed(i, JobsKey, func(i do.Injector) ([]jobs.Job, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		db, err := injection.InvokeOptional[database.Client](i)
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

		return NewJobs(ctx, cfg, db, WithPillars(pillars))
	})
}
