package saga

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	_ "github.com/primandproper/platform-go/v13/database/sqlite"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errDatabase is what the fault-injecting client returns from a statement it
// has been told to break.
var errDatabase = platformerrors.New("database is on fire")

// The store's error paths are otherwise unreachable: a real database does not
// fail on demand, and these branches decide whether a failure surfaces to the
// worker or is silently swallowed. A swallowed store error here is a saga that
// the worker believes it advanced and the row says it did not — which is the
// one disagreement this package exists to prevent.
//
// The faults are targeted rather than total, because the interesting branches
// are the ones where an earlier statement in the same operation succeeded:
// Claim's UPDATE after its SELECT, List's count after its page.

// faults says which statements to break, matched on a fragment of the query
// text. An empty faults breaks nothing.
type faults struct {
	// failExec breaks ExecContext for statements containing this fragment.
	failExec string
	// failQuery breaks QueryContext for statements containing this fragment.
	failQuery string
	// failRow breaks QueryRowContext for statements containing this fragment,
	// by serving the row from a closed pool so that Scan reports.
	failRow string
	// badResult makes ExecContext succeed for statements containing this
	// fragment while returning a Result whose RowsAffected refuses.
	badResult string
	// truncate appends LIMIT 0 to QueryContext statements containing this
	// fragment, so a read that should have returned rows returns none.
	truncate string
}

// faultyClient wraps a real client, breaking the statements faults names.
type faultyClient struct {
	database.Client

	closed *sql.DB
	faults *faults
}

var _ database.Client = (*faultyClient)(nil)

func (c *faultyClient) Reader() database.SQLQueryExecutor {
	return &faultyExecutor{inner: c.Client.Reader(), closed: c.closed, faults: c.faults}
}

func (c *faultyClient) Writer() database.SQLQueryExecutor {
	return &faultyExecutor{inner: c.Client.Writer(), closed: c.closed, faults: c.faults}
}

func (c *faultyClient) WithTransaction(ctx context.Context, fn func(database.SQLQueryExecutor) error) error {
	return c.Client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		return fn(&faultyExecutor{inner: q, closed: c.closed, faults: c.faults})
	})
}

type faultyExecutor struct {
	inner  database.SQLQueryExecutor
	closed *sql.DB
	faults *faults
}

var _ database.SQLQueryExecutor = (*faultyExecutor)(nil)

// matches reports whether a fragment is set and present in the query.
func matches(fragment, query string) bool {
	return fragment != "" && strings.Contains(query, fragment)
}

// refusingResult is a sql.Result that ran but will not say how many rows it
// touched. Real drivers do not do this; the branches that handle it are the
// difference between a guarded write reporting a miss and reporting nothing.
type refusingResult struct{}

var _ sql.Result = refusingResult{}

func (refusingResult) LastInsertId() (int64, error) { return 0, errDatabase }
func (refusingResult) RowsAffected() (int64, error) { return 0, errDatabase }

func (e *faultyExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if matches(e.faults.badResult, query) {
		return refusingResult{}, nil
	}

	if matches(e.faults.failExec, query) {
		return nil, errDatabase
	}

	return e.inner.ExecContext(ctx, query, args...)
}

func (e *faultyExecutor) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return e.inner.PrepareContext(ctx, query)
}

func (e *faultyExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if matches(e.faults.failQuery, query) {
		return nil, errDatabase
	}

	if matches(e.faults.truncate, query) {
		query += " LIMIT 0"
	}

	return e.inner.QueryContext(ctx, query, args...)
}

// QueryRowContext delegates a broken read to a closed *sql.DB, whose Row
// reports "database is closed" on Scan.
//
// A zero-value &sql.Row{} is not usable here: it has no underlying rows and
// panics rather than reporting an error, so the only way to obtain a Row
// carrying a failure is to ask a real pool that cannot serve it.
func (e *faultyExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if matches(e.faults.failRow, query) {
		return e.closed.QueryRowContext(ctx, query, args...)
	}

	return e.inner.QueryRowContext(ctx, query, args...)
}

// newFaultyStore migrates a table against the real client and returns a Store
// whose statements break according to f.
func newFaultyStore(t *testing.T, env *storeEnv, f *faults) (faulty, healthy Store) {
	t.Helper()

	// The healthy store first, so the table exists and fixtures can be written
	// through a client that works.
	healthy = env.newStore(t)

	concrete, ok := healthy.(*SQLStore)
	must.True(t, ok)

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.db"))
	must.NoError(t, err)
	must.NoError(t, db.Close())

	faulty, err = NewSQLStore(&faultyClient{Client: env.client, closed: db, faults: f},
		WithTablePrefix(concrete.tables.prefix()))
	must.NoError(t, err)

	return faulty, healthy
}

func TestSQLStore_Faults(T *testing.T) {
	T.Parallel()

	T.Run("a claim whose lease write fails reports rather than returning a batch", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		faulty, healthy := newFaultyStore(t, env, &faults{failExec: "UPDATE"})

		saveInstance(t, healthy, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)

		_, err := faulty.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Hour))
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("a claim whose read-back fails reports rather than returning a batch", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		// The projection, not the ID select: the claimable read runs first and
		// must succeed for this branch to be the one under test.
		faulty, healthy := newFaultyStore(t, env, &faults{failQuery: instanceColumns})

		saveInstance(t, healthy, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)

		_, err := faulty.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Hour))
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("a batch that shrinks between the select and the read-back is reported", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		// Simulates the instance leaving the active set between the two
		// statements — another worker's advance finishing it — which is the
		// race buildClaim repeats its status guard for and which cannot be
		// produced deterministically against a serialized database.
		faulty, healthy := newFaultyStore(t, env, &faults{truncate: instanceColumns})

		saveInstance(t, healthy, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)

		claimed, err := faulty.Claim(t.Context(), baseTime, 10, baseTime.Add(time.Hour))
		must.NoError(t, err)

		// The smaller batch is what the worker gets: it advances what it
		// actually holds rather than what it selected.
		test.SliceEmpty(t, claimed)
	})

	T.Run("a listing whose count fails reports rather than paginating on a guess", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		faulty, healthy := newFaultyStore(t, env, &faults{failRow: "COUNT(*)"})

		saveInstance(t, healthy, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)

		_, err := faulty.List(t.Context(), nil, nil)
		test.Error(t, err)
	})

	T.Run("a guarded write that will not report its row count is a failure", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		faulty, healthy := newFaultyStore(t, env, &faults{badResult: "UPDATE"})

		inst := saveInstance(t, healthy, newRecord("i1", "orders", []string{"one"}, testState{}, baseTime), baseTime)

		// Advance, through execExpectingRow.
		inst.CurrentStep = 1
		inst.UpdatedAt = baseTime

		err := faulty.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return faulty.Advance(t.Context(), q, inst, baseTime)
		})
		test.ErrorIs(t, err, errDatabase)

		// Reschedule, the same path through the writer.
		test.ErrorIs(t,
			faulty.Reschedule(t.Context(), "i1", 1, baseTime, "boom", baseTime),
			errDatabase)

		// Requeue counts rows itself rather than through execExpectingRow.
		_, err = faulty.Requeue(t.Context(), "i1", []Status{StatusRunning}, StatusCompensating, baseTime)
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("a claimable ID that will not scan is reported", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		concrete, ok := store.(*SQLStore)
		must.True(t, ok)

		// SQLite permits NULL in a TEXT PRIMARY KEY, which no code path in this
		// package can produce and which a hand-written repair script can. The
		// ID projection scans into a string, so the row has to be reported
		// rather than skipped: a claim that silently dropped it would leave an
		// advanceable saga nothing ever picks up.
		_, err := env.client.Writer().ExecContext(t.Context(),
			"INSERT INTO "+concrete.tables.instances+
				" (id, definition, status, current_step, step_names, attempts, last_error, "+
				"resume_status, started_at, updated_at, next_attempt) "+
				"VALUES (NULL, 'orders', 'running', 0, '[\"one\"]', 0, '', '', ?, ?, ?)",
			baseTime, baseTime, baseTime)
		must.NoError(t, err)

		_, err = store.Claim(t.Context(), baseTime.Add(time.Hour), 10, baseTime.Add(2*time.Hour))
		test.Error(t, err)
	})
}
