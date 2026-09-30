package scheduledjob

import (
	"context"
	"errors"
	"testing"
	"time"

	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestEnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills a job that named no frequency", func(t *testing.T) {
		t.Parallel()

		job := &jobscfg.JobConfig{}
		EnsureDefaults(job, time.Hour, time.Minute)

		test.EqOp(t, time.Hour, job.Interval)
		test.EqOp(t, time.Minute, job.LeaseTTL)
		test.NoError(t, job.ValidateWithContext(t.Context()))
	})

	T.Run("leaves a configured schedule alone", func(t *testing.T) {
		t.Parallel()

		job := &jobscfg.JobConfig{Schedule: "0 4 * * *", LeaseTTL: 5 * time.Minute}
		EnsureDefaults(job, time.Hour, time.Minute)

		test.EqOp(t, time.Duration(0), job.Interval)
		test.EqOp(t, 5*time.Minute, job.LeaseTTL)
		test.NoError(t, job.ValidateWithContext(t.Context()))
	})

	T.Run("leaves a configured interval alone", func(t *testing.T) {
		t.Parallel()

		job := &jobscfg.JobConfig{Interval: 10 * time.Minute}
		EnsureDefaults(job, time.Hour, time.Minute)

		test.EqOp(t, 10*time.Minute, job.Interval)
	})
}

func TestValidate(T *testing.T) {
	T.Parallel()

	T.Run("accepts a job its defaults have not reached yet", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, Validate(t.Context(), &jobscfg.JobConfig{}))
	})

	T.Run("still refuses a job that sets both a schedule and an interval", func(t *testing.T) {
		t.Parallel()

		test.Error(t, Validate(t.Context(), &jobscfg.JobConfig{Schedule: "* * * * *", Interval: time.Minute}))
	})

	T.Run("still refuses a schedule that does not parse", func(t *testing.T) {
		t.Parallel()

		test.Error(t, Validate(t.Context(), &jobscfg.JobConfig{Schedule: "whenever"}))
	})

	T.Run("still refuses a lease under a second", func(t *testing.T) {
		t.Parallel()

		test.Error(t, Validate(t.Context(), &jobscfg.JobConfig{LeaseTTL: time.Millisecond}))
	})
}

func TestBuild(T *testing.T) {
	T.Parallel()

	T.Run("renders an enabled job", func(t *testing.T) {
		t.Parallel()

		ran := false
		built, err := Build(&jobscfg.JobConfig{Interval: time.Minute}, "reap", func(context.Context) error {
			ran = true

			return nil
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, built)

		test.EqOp(t, "reap", built[0].Name)
		test.EqOp(t, time.Minute, built[0].Interval)
		must.NoError(t, built[0].Run(t.Context()))
		test.True(t, ran)
	})

	T.Run("renders nothing for a disabled job", func(t *testing.T) {
		t.Parallel()

		built, err := Build(&jobscfg.JobConfig{Disabled: true}, "reap", func(context.Context) error { return nil })
		must.NoError(t, err)
		test.SliceEmpty(t, built)
	})

	T.Run("reports a schedule that does not parse", func(t *testing.T) {
		t.Parallel()

		_, err := Build(&jobscfg.JobConfig{Schedule: "not cron"}, "reap", func(context.Context) error { return nil })
		test.Error(t, err)
	})
}

func TestSweep(T *testing.T) {
	T.Parallel()

	T.Run("passes the sweep's error through", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("boom")

		test.ErrorIs(t, Sweep(func(context.Context) (int64, error) { return 0, boom })(t.Context()), boom)
		test.NoError(t, Sweep(func(context.Context) (int64, error) { return 3, nil })(t.Context()))
	})
}
