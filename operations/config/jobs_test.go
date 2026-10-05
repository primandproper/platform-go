package operationscfg

import (
	"context"
	"errors"
	"testing"
	"time"

	operationsmock "github.com/primandproper/platform-go/v15/operations/mock"

	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewJobs(T *testing.T) {
	T.Parallel()

	T.Run("renders recovery and reap, each over the service, on by default", func(t *testing.T) {
		t.Parallel()

		svc := &operationsmock.ServiceMock{
			RecoverFunc: func(context.Context) (int, error) { return 2, nil },
			ReapFunc:    func(context.Context) (int64, error) { return 5, nil },
		}

		built, err := NewJobs(t.Context(), &Config{}, svc)
		must.NoError(t, err)
		must.SliceLen(t, 2, built)

		test.EqOp(t, RecoverJobName, built[0].Name)
		test.EqOp(t, DefaultRecoverInterval, built[0].Interval)
		test.EqOp(t, DefaultRecoverLeaseTTL, built[0].LeaseTTL)
		test.EqOp(t, ReapJobName, built[1].Name)
		test.EqOp(t, DefaultReapInterval, built[1].Interval)
		test.EqOp(t, DefaultReapLeaseTTL, built[1].LeaseTTL)

		must.NoError(t, built[0].Run(t.Context()))
		must.NoError(t, built[1].Run(t.Context()))

		test.SliceLen(t, 1, svc.RecoverCalls())
		test.SliceLen(t, 1, svc.ReapCalls())
	})

	T.Run("leaves out a disabled job", func(t *testing.T) {
		t.Parallel()

		built, err := NewJobs(t.Context(), &Config{Recover: jobscfg.JobConfig{Disabled: true}}, &operationsmock.ServiceMock{})
		must.NoError(t, err)
		must.SliceLen(t, 1, built)

		test.EqOp(t, ReapJobName, built[0].Name)
	})

	T.Run("keeps a schedule the deployment named", func(t *testing.T) {
		t.Parallel()

		built, err := NewJobs(t.Context(), &Config{Reap: jobscfg.JobConfig{Schedule: "0 4 * * *"}}, &operationsmock.ServiceMock{})
		must.NoError(t, err)
		must.SliceLen(t, 2, built)

		test.NotNil(t, built[1].Schedule)
		test.EqOp(t, time.Duration(0), built[1].Interval)
	})

	T.Run("a job's failure is the pass's", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("boom")

		built, err := NewJobs(t.Context(), &Config{}, &operationsmock.ServiceMock{
			RecoverFunc: func(context.Context) (int, error) { return 0, boom },
			ReapFunc:    func(context.Context) (int64, error) { return 0, boom },
		})
		must.NoError(t, err)

		for _, job := range built {
			test.ErrorIs(t, job.Run(t.Context()), boom)
		}
	})

	T.Run("refuses a job configured with both a schedule and an interval", func(t *testing.T) {
		t.Parallel()

		_, err := NewJobs(t.Context(), &Config{
			Recover: jobscfg.JobConfig{Schedule: "* * * * *", Interval: time.Minute},
		}, &operationsmock.ServiceMock{})
		test.Error(t, err)
	})

	T.Run("a nil config is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewJobs(t.Context(), nil, &operationsmock.ServiceMock{})
		test.Error(t, err)
	})

	T.Run("a nil service is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewJobs(t.Context(), &Config{}, nil)
		test.Error(t, err)
	})
}
