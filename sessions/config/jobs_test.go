package sessionscfg

import (
	"context"
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/jobs"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewJobs(T *testing.T) {
	T.Parallel()

	T.Run("renders the sweep under the database provider, on by default", func(t *testing.T) {
		t.Parallel()

		built, err := NewJobs(t.Context(), &Config{Provider: ProviderDatabase}, newTestClient(t, ""))
		must.NoError(t, err)
		must.SliceLen(t, 1, built)

		test.EqOp(t, SweepJobName, built[0].Name)
		test.EqOp(t, DefaultSweepInterval, built[0].Interval)
		test.EqOp(t, DefaultSweepJobLeaseTTL, built[0].LeaseTTL)
		test.NoError(t, built[0].Run(t.Context()))
	})

	T.Run("renders nothing under the cache provider", func(t *testing.T) {
		t.Parallel()

		built, err := NewJobs(t.Context(), memoryConfig(), nil)
		must.NoError(t, err)
		test.SliceEmpty(t, built)
	})

	T.Run("renders nothing when disabled", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderDatabase, SweepJob: jobscfg.JobConfig{Disabled: true}}

		built, err := NewJobs(t.Context(), cfg, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, built)
	})

	T.Run("a nil config is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewJobs(t.Context(), nil, nil)
		test.Error(t, err)
	})

	T.Run("a missing client under the database provider is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewJobs(t.Context(), &Config{Provider: ProviderDatabase}, nil)
		test.Error(t, err)
	})
}

func TestRegisterJobs(T *testing.T) {
	T.Parallel()

	T.Run("registers the sweep under its key", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, newTestClient(t, ""))
		do.ProvideValue(i, &Config{Provider: ProviderDatabase})
		RegisterJobs(i)

		built, err := do.InvokeNamed[[]jobs.Job](i, JobsKey)
		must.NoError(t, err)
		must.SliceLen(t, 1, built)
		test.EqOp(t, SweepJobName, built[0].Name)
	})
}
