package retentioncfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/retention"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterSweeper(T *testing.T) {
	T.Parallel()

	T.Run("resolves a sweeper from a container carrying only the essentials", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{})
		do.ProvideValue[database.Client](i, newTestClient(t))
		do.ProvideValue(i, testPolicies())

		RegisterSweeper(i)

		sweeper, err := do.Invoke[*retention.Sweeper](i)
		must.NoError(t, err)
		must.NotNil(t, sweeper)
	})

	T.Run("a container with no observability still wires up", func(t *testing.T) {
		t.Parallel()

		// Absent is fine; registered-but-failing is not. That is the whole
		// distinction observability.InvokePillars draws.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{})
		do.ProvideValue[database.Client](i, newTestClient(t))
		do.ProvideValue(i, testPolicies())

		RegisterSweeper(i)

		_, err := do.Invoke[*retention.Sweeper](i)
		test.NoError(t, err)
	})

	T.Run("picks up a registered audit recorder", func(t *testing.T) {
		t.Parallel()

		recorder, err := audit.NewRecorder(dialect.SQLite)
		must.NoError(t, err)

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{})
		do.ProvideValue[database.Client](i, newTestClient(t))
		do.ProvideValue(i, testPolicies())
		do.ProvideValue[audit.Recorder](i, recorder)

		RegisterSweeper(i)

		sweeper, err := do.Invoke[*retention.Sweeper](i)
		must.NoError(t, err)
		must.NotNil(t, sweeper)
	})

	T.Run("fails when the policies are not registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{})
		do.ProvideValue[database.Client](i, newTestClient(t))

		RegisterSweeper(i)

		_, err := do.Invoke[*retention.Sweeper](i)
		test.Error(t, err)
	})
}
