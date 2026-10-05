package signincfg

import (
	"context"
	"errors"
	"testing"

	refreshtokenmigrations "github.com/primandproper/platform-go/v15/authentication/signin/refreshtokens/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/jobs"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewJobs(T *testing.T) {
	T.Parallel()

	T.Run("renders the refresh token sweep, on by default", func(t *testing.T) {
		t.Parallel()

		calls := 0
		built, err := NewJobs(t.Context(), &Config{DefaultOwnerRoles: ownerRoles, Registration: RegistrationConfig{Disabled: true}},
			func(context.Context) (int64, error) {
				calls++

				return 0, nil
			})
		must.NoError(t, err)
		must.SliceLen(t, 1, built)

		test.EqOp(t, RefreshTokenSweepJobName, built[0].Name)
		test.EqOp(t, DefaultSweepInterval, built[0].Interval)
		test.EqOp(t, DefaultSweepJobLeaseTTL, built[0].LeaseTTL)

		must.NoError(t, built[0].Run(t.Context()))
		test.EqOp(t, 1, calls)
	})

	T.Run("a sweep's failure is the job's", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("boom")

		built, err := NewJobs(t.Context(), &Config{DefaultOwnerRoles: ownerRoles}, func(context.Context) (int64, error) { return 0, boom })
		must.NoError(t, err)
		test.ErrorIs(t, built[0].Run(t.Context()), boom)
	})

	T.Run("renders nothing when disabled", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{DefaultOwnerRoles: ownerRoles, RefreshTokens: RefreshTokensConfig{SweepJob: jobscfg.JobConfig{Disabled: true}}}

		built, err := NewJobs(t.Context(), cfg, func(context.Context) (int64, error) { return 0, nil })
		must.NoError(t, err)
		test.SliceEmpty(t, built)
	})

	T.Run("refuses a nil config or sweep", func(t *testing.T) {
		t.Parallel()

		_, err := NewJobs(t.Context(), nil, func(context.Context) (int64, error) { return 0, nil })
		test.Error(t, err)

		_, err = NewJobs(t.Context(), &Config{DefaultOwnerRoles: ownerRoles}, nil)
		test.Error(t, err)
	})
}

func TestRegisterJobs(T *testing.T) {
	T.Parallel()

	T.Run("sweeps the refresh token table through a handle of its own", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{DefaultOwnerRoles: ownerRoles})
		RegisterJobs(i)

		client := do.MustInvoke[database.Client](i)

		stmts, err := refreshtokenmigrations.Statements(dialect.SQLite, "")
		must.NoError(t, err)

		for _, stmt := range stmts {
			_, err = client.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, err)
		}

		built, err := do.InvokeNamed[[]jobs.Job](i, JobsKey)
		must.NoError(t, err)
		must.SliceLen(t, 1, built)

		test.EqOp(t, RefreshTokenSweepJobName, built[0].Name)
		test.NoError(t, built[0].Run(t.Context()))
	})

	T.Run("a disabled job needs no client", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{DefaultOwnerRoles: ownerRoles, RefreshTokens: RefreshTokensConfig{SweepJob: jobscfg.JobConfig{Disabled: true}}})
		RegisterJobs(i)

		built, err := do.InvokeNamed[[]jobs.Job](i, JobsKey)
		must.NoError(t, err)
		test.SliceEmpty(t, built)
	})
}
