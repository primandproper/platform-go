package saga

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// bogusDialectClient reports a dialect this package cannot emit SQL for.
//
// The unsupported-dialect branch is otherwise unreachable: the dialect comes
// from the client rather than the caller, and every client this module ships
// reports one of the three supported dialects. Only Dialect is consulted before
// the constructor gives up, so the embedded Client is never called.
type bogusDialectClient struct {
	database.Client
}

func (bogusDialectClient) Dialect() dialect.Dialect { return "oracle" }

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		_, err := NewSQLStore(bogusDialectClient{env.client})
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("rejects a table prefix that is not an identifier", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		_, err := NewSQLStore(env.client, WithTablePrefix("drop table;--"))
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
	})

	T.Run("ignores nil and empty options", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client,
			nil,
			WithTablePrefix(""),
			WithStoreLogger(nil),
			WithStoreTracerProvider(nil),
			WithStoreMetricsProvider(nil),
		)
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}

// runStoreSuite is the behavioral suite every dialect must satisfy. SQLite runs
// it here; the container tests run the same thing against Postgres and MySQL.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("save and get", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"a", "b"}, testState{Amount: 3}, baseTime), baseTime)

		got, err := store.Get(t.Context(), inst.ID)
		must.NoError(t, err)

		test.EqOp(t, "i1", got.ID)
		test.EqOp(t, "orders", got.Definition)
		test.EqOp(t, StatusRunning, got.Status)
		test.EqOp(t, 0, got.CurrentStep)
		test.EqOp(t, 0, got.Attempts)
		test.Eq(t, []string{"a", "b"}, got.StepNames)
		test.EqOp(t, baseTime, got.StartedAt)
		test.Eq(t, []byte(`{"trail":null,"amount":3}`), []byte(got.State))
	})

	t.Run("get reports a missing instance", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.Get(t.Context(), "nope")
		test.ErrorIs(t, err, ErrInstanceNotFound)
	})

	t.Run("save refuses a nil executor and a nil instance", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		test.ErrorIs(t, store.Save(t.Context(), nil, &Record{}, baseTime), ErrNilExecutor)

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			test.ErrorIs(t, store.Save(t.Context(), q, nil, baseTime), ErrNilInstance)

			return nil
		}))
	})

	t.Run("claim leases due instances and increments attempts", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saveInstance(t, store, newRecord("due", "orders", []string{"a"}, testState{}, baseTime), baseTime)
		saveInstance(t, store, newRecord("later", "orders", []string{"a"}, testState{}, baseTime), baseTime.Add(time.Hour))

		claimed, err := store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		test.EqOp(t, "due", claimed[0].ID)
		test.EqOp(t, 1, claimed[0].Attempts)

		// Leased, so a second claim at the same instant sees nothing.
		again, err := store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Minute))
		must.NoError(t, err)
		test.SliceEmpty(t, again)

		// Once the lease lapses it comes back, with another attempt spent.
		reclaimed, err := store.Claim(t.Context(), baseTime.Add(2*time.Minute), 10, baseTime.Add(3*time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 1, reclaimed)
		test.EqOp(t, 2, reclaimed[0].Attempts)
	})

	t.Run("claim skips terminal instances", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		inst := saveInstance(t, store, newRecord("done", "orders", []string{"a"}, testState{}, baseTime), baseTime)

		inst.Status = StatusCompleted
		inst.UpdatedAt = baseTime
		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Advance(t.Context(), q, inst, baseTime)
		}))

		claimed, err := store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Minute))
		must.NoError(t, err)
		test.SliceEmpty(t, claimed)
	})

	t.Run("claim returns nothing for a non-positive limit", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		claimed, err := store.Claim(t.Context(), baseTime, 0, baseTime)
		must.NoError(t, err)
		test.SliceEmpty(t, claimed)
	})

	t.Run("advance moves the cursor and keeps the lease mid-pass", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"a", "b"}, testState{}, baseTime), baseTime)

		claimed, err := store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Hour))
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		inst.CurrentStep = 1
		inst.State = []byte(`{"amount":9}`)
		inst.UpdatedAt = baseTime.Add(time.Second)

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Advance(t.Context(), q, inst, baseTime.Add(time.Second))
		}))

		got, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, 1, got.CurrentStep)
		test.EqOp(t, 0, got.Attempts)
		test.Eq(t, []byte(`{"amount":9}`), []byte(got.State))

		// The lease was kept, so nothing else can claim it.
		claimedAgain, err := store.Claim(t.Context(), baseTime.Add(time.Second), 10, baseTime.Add(time.Hour))
		must.NoError(t, err)
		test.SliceEmpty(t, claimedAgain)
	})

	t.Run("advance drops the lease when a delay is scheduled", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"a", "b"}, testState{}, baseTime), baseTime)

		_, err := store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Hour))
		must.NoError(t, err)

		inst.CurrentStep = 1
		inst.UpdatedAt = baseTime

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Advance(t.Context(), q, inst, baseTime.Add(time.Minute))
		}))

		// Not claimable before the delay elapses...
		early, err := store.Claim(t.Context(), baseTime.Add(30*time.Second), 10, baseTime.Add(time.Hour))
		must.NoError(t, err)
		test.SliceEmpty(t, early)

		// ...and claimable straight after it, without waiting out the lease.
		due, err := store.Claim(t.Context(), baseTime.Add(time.Minute), 10, baseTime.Add(2*time.Hour))
		must.NoError(t, err)
		test.SliceLen(t, 1, due)
	})

	t.Run("advance refuses to move a terminal instance", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"a"}, testState{}, baseTime), baseTime)

		inst.Status = StatusCompleted
		inst.UpdatedAt = baseTime
		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Advance(t.Context(), q, inst, baseTime)
		}))

		inst.Status = StatusRunning
		err := store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Advance(t.Context(), q, inst, baseTime)
		})
		test.ErrorIs(t, err, ErrInstanceNotFound)
	})

	t.Run("advance refuses a nil executor and a nil instance", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		test.ErrorIs(t, store.Advance(t.Context(), nil, &Record{}, baseTime), ErrNilExecutor)

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			test.ErrorIs(t, store.Advance(t.Context(), q, nil, baseTime), ErrNilInstance)

			return nil
		}))
	})

	t.Run("reschedule records the attempt and drops the lease", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		saveInstance(t, store, newRecord("i1", "orders", []string{"a"}, testState{}, baseTime), baseTime)

		_, err := store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Hour))
		must.NoError(t, err)

		must.NoError(t, store.Reschedule(t.Context(), "i1", 3, baseTime.Add(time.Minute), "the card was declined", baseTime))

		got, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, 3, got.Attempts)
		test.EqOp(t, "the card was declined", got.LastError)

		due, err := store.Claim(t.Context(), baseTime.Add(time.Minute), 10, baseTime.Add(2*time.Hour))
		must.NoError(t, err)
		test.SliceLen(t, 1, due)
	})

	t.Run("reschedule reports an instance that is no longer active", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		err := store.Reschedule(t.Context(), "ghost", 1, baseTime, "gone", baseTime)
		test.ErrorIs(t, err, ErrInstanceNotFound)
	})

	t.Run("release hands the lease back without moving anything", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		saveInstance(t, store, newRecord("i1", "orders", []string{"a"}, testState{}, baseTime), baseTime)

		_, err := store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Hour))
		must.NoError(t, err)

		must.NoError(t, store.Release(t.Context(), "i1", baseTime))

		again, err := store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Hour))
		must.NoError(t, err)
		must.SliceLen(t, 1, again)
		test.EqOp(t, 2, again[0].Attempts)
	})

	t.Run("requeue moves a stuck instance back and clears its resume status", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		inst := saveInstance(t, store, newRecord("i1", "orders", []string{"a"}, testState{}, baseTime), baseTime)

		inst.Status = StatusStuck
		inst.ResumeStatus = StatusCompensating
		inst.LastError = "the refund failed"
		inst.UpdatedAt = baseTime
		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Advance(t.Context(), q, inst, baseTime)
		}))

		stuck, err := store.Get(t.Context(), "i1")
		must.NoError(t, err)
		test.EqOp(t, StatusStuck, stuck.Status)
		test.EqOp(t, StatusCompensating, stuck.ResumeStatus)

		updated, err := store.Requeue(t.Context(), "i1", []Status{StatusStuck}, StatusCompensating, baseTime.Add(time.Minute))
		must.NoError(t, err)
		test.EqOp(t, StatusCompensating, updated.Status)
		test.EqOp(t, Status(""), updated.ResumeStatus)
		test.EqOp(t, "the refund failed", updated.LastError)

		// Immediately claimable again.
		claimed, err := store.Claim(t.Context(), baseTime.Add(time.Minute), 10, baseTime.Add(time.Hour))
		must.NoError(t, err)
		test.SliceLen(t, 1, claimed)
	})

	t.Run("requeue reports an instance in the wrong status", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		saveInstance(t, store, newRecord("i1", "orders", []string{"a"}, testState{}, baseTime), baseTime)

		_, err := store.Requeue(t.Context(), "i1", []Status{StatusStuck}, StatusRunning, baseTime)
		test.ErrorIs(t, err, ErrInstanceNotFound)
	})

	t.Run("requeue refuses an empty source status set", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.Requeue(t.Context(), "i1", nil, StatusRunning, baseTime)
		test.Error(t, err)
	})

	t.Run("list narrows by definition and status", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saveInstance(t, store, newRecord("a1", "orders", []string{"a"}, testState{}, baseTime), baseTime)
		saveInstance(t, store, newRecord("a2", "orders", []string{"a"}, testState{}, baseTime), baseTime)
		saveInstance(t, store, newRecord("b1", "refunds", []string{"a"}, testState{}, baseTime), baseTime)

		stuck := newRecord("a3", "orders", []string{"a"}, testState{}, baseTime)
		saveInstance(t, store, stuck, baseTime)
		stuck.Status = StatusStuck
		stuck.UpdatedAt = baseTime
		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Advance(t.Context(), q, stuck, baseTime)
		}))

		all, err := store.List(t.Context(), nil, nil)
		must.NoError(t, err)
		test.SliceLen(t, 4, all.Data)
		test.EqOp(t, uint64(4), all.TotalCount)

		byDefinition, err := store.List(t.Context(), &ListScope{Definition: "orders"}, nil)
		must.NoError(t, err)
		test.SliceLen(t, 3, byDefinition.Data)

		byStatus, err := store.List(t.Context(), &ListScope{Statuses: []Status{StatusStuck}}, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, byStatus.Data)
		test.EqOp(t, "a3", byStatus.Data[0].ID)

		both, err := store.List(t.Context(), &ListScope{
			Definition: "refunds",
			Statuses:   []Status{StatusStuck},
		}, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, both.Data)
	})

	t.Run("list paginates and sorts", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		for _, id := range []string{"a", "b", "c"} {
			saveInstance(t, store, newRecord(id, "orders", []string{"one"}, testState{}, baseTime), baseTime)
		}

		filter := filtering.DefaultQueryFilter()
		filter.MaxResponseSize = pointer.To(uint16(2))

		first, err := store.List(t.Context(), nil, filter)
		must.NoError(t, err)
		must.SliceLen(t, 2, first.Data)
		test.EqOp(t, "a", first.Data[0].ID)
		test.EqOp(t, "b", first.Data[1].ID)
		test.EqOp(t, "b", first.Cursor)

		filter.Cursor = pointer.To(first.Cursor)

		second, err := store.List(t.Context(), nil, filter)
		must.NoError(t, err)
		must.SliceLen(t, 1, second.Data)
		test.EqOp(t, "c", second.Data[0].ID)

		descending := filtering.DefaultQueryFilter()
		descending.SortBy = filtering.SortDescending

		reversed, err := store.List(t.Context(), nil, descending)
		must.NoError(t, err)
		must.SliceLen(t, 3, reversed.Data)
		test.EqOp(t, "c", reversed.Data[0].ID)
	})

	t.Run("a claimed batch does not share one state buffer", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saveInstance(t, store, newRecord("i1", "orders", []string{"a"}, testState{Amount: 1}, baseTime), baseTime)
		saveInstance(t, store, newRecord("i2", "orders", []string{"a"}, testState{Amount: 2}, baseTime), baseTime)

		claimed, err := store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Hour))
		must.NoError(t, err)
		must.SliceLen(t, 2, claimed)

		// database/sql reuses the byte slice backing a []byte destination across
		// Next calls, so without a copy both instances would carry the last
		// row's state.
		test.NotEq(t, []byte(claimed[0].State), []byte(claimed[1].State))
	})
}

func TestSQLStore(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

func TestSQLStore_Errors(T *testing.T) {
	T.Parallel()

	T.Run("reports a query against a table that is not there", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client, WithTablePrefix("absent"))
		must.NoError(t, err)

		_, err = store.Get(t.Context(), "i1")
		test.Error(t, err)

		_, err = store.List(t.Context(), nil, nil)
		test.Error(t, err)

		_, err = store.Claim(t.Context(), baseTime, 1, baseTime)
		test.Error(t, err)

		test.Error(t, store.Release(t.Context(), "i1", baseTime))
		test.Error(t, store.Reschedule(t.Context(), "i1", 1, baseTime, "", baseTime))

		_, err = store.Requeue(t.Context(), "i1", []Status{StatusStuck}, StatusRunning, baseTime)
		test.Error(t, err)

		test.Error(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Save(t.Context(), q, newRecord("i1", "orders", []string{"a"}, testState{}, baseTime), baseTime)
		}))
	})

	T.Run("reports step names that will not decode", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		saveInstance(t, store, newRecord("i1", "orders", []string{"a"}, testState{}, baseTime), baseTime)

		// Reaching past the store to corrupt the column, which is the only way
		// this row ever gets written: nothing in the package writes a non-JSON
		// step list.
		concrete, ok := store.(*SQLStore)
		must.True(t, ok)

		_, err := env.client.Writer().ExecContext(t.Context(),
			"UPDATE "+concrete.tables.instances+" SET step_names = 'not json' WHERE id = 'i1'")
		must.NoError(t, err)

		_, err = store.Get(t.Context(), "i1")
		test.Error(t, err)

		// The same corruption reached through the projection that drains many
		// rows rather than one, which is a different scan path.
		_, err = store.List(t.Context(), nil, nil)
		test.Error(t, err)

		_, err = store.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Hour))
		test.Error(t, err)
	})
}

func TestStatus(T *testing.T) {
	T.Parallel()

	T.Run("terminal", func(t *testing.T) {
		t.Parallel()

		for status, want := range map[Status]bool{
			StatusRunning:      false,
			StatusCompensating: false,
			StatusCompleted:    true,
			StatusCompensated:  true,
			StatusStuck:        true,
			Status("nonsense"): false,
		} {
			test.EqOp(t, want, status.Terminal(), test.Sprintf("status %q", status))
		}
	})

	T.Run("valid", func(t *testing.T) {
		t.Parallel()

		for status, want := range map[Status]bool{
			StatusRunning:      true,
			StatusCompensating: true,
			StatusCompleted:    true,
			StatusCompensated:  true,
			StatusStuck:        true,
			Status(""):         false,
			Status("nonsense"): false,
		} {
			test.EqOp(t, want, status.Valid(), test.Sprintf("status %q", status))
		}
	})
}

func TestStatusStrings(T *testing.T) {
	T.Parallel()

	T.Run("renders a status set for a span", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "running,compensating", statusStrings(activeStatuses))
		test.EqOp(t, "", statusStrings(nil))
	})
}
