package passwordresetcfg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	passwordresetmock "github.com/primandproper/platform-go/v14/authentication/passwordreset/mock"

	"github.com/primandproper/primitives-go/v2/jobs"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewJobs(T *testing.T) {
	T.Parallel()

	T.Run("renders the sweep, on by default", func(t *testing.T) {
		t.Parallel()

		calls := 0
		built, err := NewJobs(t.Context(), &Config{}, func(context.Context) (int64, error) {
			calls++

			return 4, nil
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, built)

		test.EqOp(t, SweepJobName, built[0].Name)
		test.EqOp(t, DefaultSweepInterval, built[0].Interval)
		test.EqOp(t, DefaultSweepJobLeaseTTL, built[0].LeaseTTL)

		must.NoError(t, built[0].Run(t.Context()))
		test.EqOp(t, 1, calls)
	})

	T.Run("a sweep's failure is the job's", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("boom")

		built, err := NewJobs(t.Context(), &Config{}, func(context.Context) (int64, error) { return 0, boom })
		must.NoError(t, err)
		test.ErrorIs(t, built[0].Run(t.Context()), boom)
	})

	T.Run("renders nothing when disabled", func(t *testing.T) {
		t.Parallel()

		built, err := NewJobs(t.Context(), &Config{SweepJob: jobscfg.JobConfig{Disabled: true}},
			func(context.Context) (int64, error) { return 0, nil })
		must.NoError(t, err)
		test.SliceEmpty(t, built)
	})

	T.Run("refuses a schedule that does not parse", func(t *testing.T) {
		t.Parallel()

		_, err := NewJobs(t.Context(), &Config{SweepJob: jobscfg.JobConfig{Schedule: "whenever", Interval: time.Hour}},
			func(context.Context) (int64, error) { return 0, nil })
		test.Error(t, err)
	})

	T.Run("refuses a nil config or sweep", func(t *testing.T) {
		t.Parallel()

		_, err := NewJobs(t.Context(), nil, func(context.Context) (int64, error) { return 0, nil })
		test.Error(t, err)

		_, err = NewJobs(t.Context(), &Config{}, nil)
		test.Error(t, err)
	})
}

func TestRegisterJobs(T *testing.T) {
	T.Parallel()

	T.Run("schedules the registered SQL store's sweep", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{})
		RegisterStore(i)
		RegisterJobs(i)

		built, err := do.InvokeNamed[[]jobs.Job](i, JobsKey)
		must.NoError(t, err)
		must.SliceLen(t, 1, built)
		test.EqOp(t, SweepJobName, built[0].Name)
	})

	T.Run("a store with no Sweep is an error, not a quiet skip", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{})
		do.ProvideValue[passwordreset.Store](i, &passwordresetmock.StoreMock{})
		RegisterJobs(i)

		_, err := do.InvokeNamed[[]jobs.Job](i, JobsKey)
		test.Error(t, err)
	})

	T.Run("a disabled job needs no store at all", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{SweepJob: jobscfg.JobConfig{Disabled: true}})
		RegisterJobs(i)

		built, err := do.InvokeNamed[[]jobs.Job](i, JobsKey)
		must.NoError(t, err)
		test.SliceEmpty(t, built)
	})
}
