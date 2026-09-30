package oauth2serverstorecfg

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/jobs"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// sweepingStore is an oauth2server.Store whose Sweep is the only method any
// test here reaches. The embedded interface is nil, so any other call panics
// rather than answering.
type sweepingStore struct {
	oauth2server.Store

	err   error
	calls int
}

func (s *sweepingStore) Sweep(context.Context) (int64, error) {
	s.calls++

	return 1, s.err
}

func TestNewJobs(T *testing.T) {
	T.Parallel()

	T.Run("renders the sweep under the database provider, on by default", func(t *testing.T) {
		t.Parallel()

		store := &sweepingStore{}

		built, err := NewJobs(t.Context(), &Config{Provider: ProviderDatabase}, store)
		must.NoError(t, err)
		must.SliceLen(t, 1, built)

		test.EqOp(t, SweepJobName, built[0].Name)
		test.EqOp(t, oauth2server.DefaultSweepInterval, built[0].Interval)
		test.EqOp(t, DefaultSweepJobLeaseTTL, built[0].LeaseTTL)

		must.NoError(t, built[0].Run(t.Context()))
		test.EqOp(t, 1, store.calls)
	})

	T.Run("a sweep's failure is the job's", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("boom")

		built, err := NewJobs(t.Context(), &Config{}, &sweepingStore{err: boom})
		must.NoError(t, err)
		must.SliceLen(t, 1, built)
		test.ErrorIs(t, built[0].Run(t.Context()), boom)
	})

	T.Run("renders nothing under the memory provider", func(t *testing.T) {
		t.Parallel()

		built, err := NewJobs(t.Context(), &Config{Provider: ProviderMemory}, &sweepingStore{})
		must.NoError(t, err)
		test.SliceEmpty(t, built)
	})

	T.Run("renders nothing when disabled", func(t *testing.T) {
		t.Parallel()

		built, err := NewJobs(t.Context(), &Config{SweepJob: jobscfg.JobConfig{Disabled: true}}, &sweepingStore{})
		must.NoError(t, err)
		test.SliceEmpty(t, built)
	})

	T.Run("refuses a nil config or store", func(t *testing.T) {
		t.Parallel()

		_, err := NewJobs(t.Context(), nil, &sweepingStore{})
		test.Error(t, err)

		_, err = NewJobs(t.Context(), &Config{}, nil)
		test.Error(t, err)
	})
}

func TestRegisterJobs(T *testing.T) {
	T.Parallel()

	T.Run("schedules the registered store's sweep", func(t *testing.T) {
		t.Parallel()

		i := newInjector(t, &Config{Provider: ProviderDatabase})
		do.ProvideValue[oauth2server.Store](i, &sweepingStore{})
		RegisterJobs(i)

		built, err := do.InvokeNamed[[]jobs.Job](i, JobsKey)
		must.NoError(t, err)
		must.SliceLen(t, 1, built)
		test.EqOp(t, SweepJobName, built[0].Name)
	})
}
