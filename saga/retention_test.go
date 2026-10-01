package saga

import (
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/retention"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var _ retention.Target = RetentionTarget{}

// finishAt moves a saved instance into status, stamped at at, through the
// store's own guarded advance — the same write a worker makes when a saga
// reaches that status.
func (e *storeEnv) finishAt(t *testing.T, store Store, inst *Record, status Status, at time.Time) {
	t.Helper()

	inst.Status = status

	must.NoError(t, e.client.WithTransaction(t.Context(), func(q database.Tx) error {
		return store.Advance(t.Context(), q, inst, at, at)
	}))
}

// retentionPolicies is the pair sagacfg builds, over the store's table.
func retentionPolicies(store Store) []retention.Policy {
	prefix := store.(*SQLStore).prefix

	return []retention.Policy{
		{
			Name:   "saga-completed",
			Target: RetentionTarget{TablePrefix: prefix, Status: StatusCompleted},
			Scope:  tenancy.Global(),
			Age:    DefaultCompletedRetention,
		},
		{
			Name:   "saga-compensated",
			Target: RetentionTarget{TablePrefix: prefix, Status: StatusCompensated},
			Scope:  tenancy.Global(),
			Age:    DefaultCompensatedRetention,
		},
	}
}

func runRetentionSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("removes only terminal instances past their window", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		now := newStubClock()

		old := baseTime.Add(-60 * 24 * time.Hour)
		recent := baseTime.Add(-10 * 24 * time.Hour)

		save := func(id string, status Status, at time.Time) {
			inst := env.saveInstance(t, store, newRecord(id, "orders", []string{"a"}, testState{}, old), old)
			env.finishAt(t, store, inst, status, at)
		}

		// Past both windows, in every status. Only the two terminal statuses a
		// retention pass may remove go; the three a worker or an operator still
		// owns stay, however old.
		save("completed-old", StatusCompleted, old)
		save("compensated-old", StatusCompensated, old)
		save("running-old", StatusRunning, old)
		save("compensating-old", StatusCompensating, old)
		save("stuck-old", StatusStuck, old)

		// Ten days old: past the completed window, inside the compensated one.
		save("completed-recent", StatusCompleted, recent)
		save("compensated-recent", StatusCompensated, recent)

		// Never advanced, so last_updated_at is NULL: never a finished saga.
		env.saveInstance(t, store, newRecord("never-advanced", "orders", []string{"a"}, testState{}, old), old)

		sweeper, err := retention.NewSweeper(t.Context(), &retention.SweeperConfig{}, env.client,
			retentionPolicies(store), retention.WithSweeperClock(now))
		must.NoError(t, err)

		result, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(3), result.Removed)

		for _, gone := range []string{"completed-old", "compensated-old", "completed-recent"} {
			_, getErr := store.Get(t.Context(), gone)
			test.ErrorIs(t, getErr, ErrInstanceNotFound, test.Sprintf("%s should have been removed", gone))
		}

		for _, kept := range []string{
			"running-old", "compensating-old", "stuck-old", "compensated-recent", "never-advanced",
		} {
			_, getErr := store.Get(t.Context(), kept)
			test.NoError(t, getErr, test.Sprintf("%s should have survived", kept))
		}

		// And the backlog reads zero once drained, which it would not if the
		// count selected rows the delete does not take.
		for i := range result.Policies {
			test.EqOp(t, int64(0), result.Policies[i].Backlog, test.Sprintf("policy %s", result.Policies[i].Name))
		}
	})

	t.Run("a batch removes no more than its limit, oldest first", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		target := RetentionTarget{TablePrefix: store.(*SQLStore).prefix, Status: StatusCompleted}

		for i, id := range []string{"c1", "c2", "c3"} {
			at := baseTime.Add(-time.Duration(30-i) * 24 * time.Hour)
			inst := env.saveInstance(t, store, newRecord(id, "orders", []string{"a"}, testState{}, at), at)
			env.finishAt(t, store, inst, StatusCompleted, at)
		}

		backlog, err := target.Backlog(t.Context(), env.client.Reader(), env.dialect, baseTime, 2)
		must.NoError(t, err)
		test.EqOp(t, int64(2), backlog)

		var removed int64

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			var sweepErr error
			removed, sweepErr = target.Sweep(t.Context(), q, env.dialect, baseTime, 2)

			return sweepErr
		}))

		test.EqOp(t, int64(2), removed)

		_, err = store.Get(t.Context(), "c3")
		test.NoError(t, err)
	})
}

func TestRetentionTarget(T *testing.T) {
	T.Parallel()

	runRetentionSuite(T, newSQLiteEnv(T))
}

func TestRetentionTarget_Validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts the two terminal statuses a pass may remove", func(t *testing.T) {
		t.Parallel()

		for _, status := range []Status{StatusCompleted, StatusCompensated} {
			test.NoError(t, RetentionTarget{Status: status}.Validate(dialect.Postgres))
		}
	})

	T.Run("refuses every status a worker or an operator still owns", func(t *testing.T) {
		t.Parallel()

		for _, status := range []Status{StatusRunning, StatusCompensating, StatusStuck, "bogus"} {
			test.ErrorIs(t, RetentionTarget{Status: status}.Validate(dialect.Postgres), ErrUnretirableStatus)
		}
	})

	T.Run("refuses an unknown dialect", func(t *testing.T) {
		t.Parallel()

		err := RetentionTarget{Status: StatusCompleted}.Validate("oracle")
		test.True(t, errors.Is(err, dialect.ErrUnsupported))
	})

	T.Run("refuses an invalid prefix", func(t *testing.T) {
		t.Parallel()

		test.Error(t, RetentionTarget{TablePrefix: "bad prefix;", Status: StatusCompleted}.Validate(dialect.Postgres))
	})

	T.Run("describes the table and the status", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "ddb_saga_instances (completed)",
			RetentionTarget{TablePrefix: "ddb", Status: StatusCompleted}.Describe())
	})
}
