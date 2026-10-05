package service

import (
	"context"
	"testing"
	"time"

	oauth2serverstorecfg "github.com/primandproper/platform-go/v15/authentication/oauth2serverstore/config"
	"github.com/primandproper/platform-go/v15/operations"
	operationscfg "github.com/primandproper/platform-go/v15/operations/config"
	operationsmock "github.com/primandproper/platform-go/v15/operations/mock"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/jobs"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// journalingOperations is an operations.Service whose two reapers journal that
// they ran, and whose every other method is the mock's own panic.
func journalingOperations(j *journal) *operationsmock.ServiceMock {
	return &operationsmock.ServiceMock{
		RecoverFunc: func(context.Context) (int, error) {
			j.record("job:recover")

			return 0, nil
		},
		ReapFunc: func(context.Context) (int64, error) {
			j.record("job:reap")

			return 0, nil
		},
	}
}

func TestService_PlatformJobs(T *testing.T) {
	T.Parallel()

	T.Run("a registered operations service gets Recover and Reap scheduled", func(t *testing.T) {
		t.Parallel()

		j := &journal{}
		i := lifecycleInjector(t, j, nil)

		// RunOnStart so the scheduler fires both the moment it runs, rather
		// than an interval later; nothing else about the jobs is the test's.
		do.ProvideValue(i, scheduler(t))
		do.ProvideValue(i, &operationscfg.Config{
			Recover: jobscfg.JobConfig{RunOnStart: true},
			Reap:    jobscfg.JobConfig{RunOnStart: true},
		})
		do.ProvideValue[operations.Service](i, journalingOperations(j))
		operationscfg.RegisterJobs(i)

		svc, err := New(i)
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())

		errs := make(chan error, 1)
		go func() { errs <- svc.Run(ctx) }()

		waitFor(t, j, "job:recover")
		waitFor(t, j, "job:reap")
		cancel()

		must.NoError(t, <-errs)
	})

	T.Run("the platform's jobs share one registration with the application's", func(t *testing.T) {
		t.Parallel()

		i := lifecycleInjector(t, &journal{}, nil)

		// The application naming one of its own jobs after a platform reaper
		// is the collision a second Register call would have let half through.
		do.ProvideValue(i, scheduler(t))
		do.ProvideValue(i, []jobs.Job{{
			Name:     operationscfg.ReapJobName,
			Interval: time.Hour,
			Run:      func(context.Context) error { return nil },
		}})
		do.ProvideValue(i, &operationscfg.Config{})
		do.ProvideValue[operations.Service](i, &operationsmock.ServiceMock{})
		operationscfg.RegisterJobs(i)

		_, err := New(i)
		test.ErrorIs(t, err, jobs.ErrDuplicateJob)
	})

	T.Run("a reaper with no other way to run fails the startup without a scheduler", func(t *testing.T) {
		t.Parallel()

		i := lifecycleInjector(t, &journal{}, nil)
		do.ProvideValue(i, &operationscfg.Config{})
		do.ProvideValue[operations.Service](i, &operationsmock.ServiceMock{})
		operationscfg.RegisterJobs(i)

		_, err := New(i)
		test.ErrorIs(t, err, ErrScheduledJobsWithoutScheduler)
	})

	T.Run("a reaper switched off by name needs no scheduler", func(t *testing.T) {
		t.Parallel()

		i := lifecycleInjector(t, &journal{}, nil)
		do.ProvideValue(i, &operationscfg.Config{
			Recover: jobscfg.JobConfig{Disabled: true},
			Reap:    jobscfg.JobConfig{Disabled: true},
		})
		do.ProvideValue[operations.Service](i, &operationsmock.ServiceMock{})
		operationscfg.RegisterJobs(i)

		_, err := New(i)
		test.NoError(t, err)
	})

	T.Run("a sweep that runs in-process without a scheduler is not built without one", func(t *testing.T) {
		t.Parallel()

		i := lifecycleInjector(t, &journal{}, nil)

		// No oauth2server.Store is registered, so building the job would fail:
		// that New succeeds is the evidence it was skipped rather than built.
		do.ProvideValue(i, &oauth2serverstorecfg.Config{Provider: oauth2serverstorecfg.ProviderDatabase})
		oauth2serverstorecfg.RegisterJobs(i)

		_, err := New(i)
		test.NoError(t, err)
	})

	T.Run("a sweep with an in-process fallback is scheduled when there is a scheduler", func(t *testing.T) {
		t.Parallel()

		j := &journal{}
		i := lifecycleInjector(t, j, nil)
		do.ProvideValue(i, scheduler(t))
		do.ProvideValue(i, &oauth2serverstorecfg.Config{
			Provider: oauth2serverstorecfg.ProviderDatabase,
			SweepJob: jobscfg.JobConfig{RunOnStart: true},
		})
		do.ProvideValue[oauth2server.Store](i, &journalingOAuth2Store{j: j})
		oauth2serverstorecfg.RegisterJobs(i)

		svc, err := New(i)
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())

		errs := make(chan error, 1)
		go func() { errs <- svc.Run(ctx) }()

		waitFor(t, j, "job:oauth2-sweep")
		cancel()

		must.NoError(t, <-errs)
	})
}

// journalingOAuth2Store is an oauth2server.Store whose Sweep journals that it
// ran. The embedded interface is nil, so any other call panics.
type journalingOAuth2Store struct {
	oauth2server.Store

	j *journal
}

func (s *journalingOAuth2Store) Sweep(context.Context) (int64, error) {
	s.j.record("job:oauth2-sweep")

	return 0, nil
}
