package dataprivacy

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	_ "github.com/primandproper/platform-go/v13/database/sqlite"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errDatabase is what the fault-injecting client returns from every statement.
var errDatabase = platformerrors.New("database is on fire")

// failingClient is a database.Client whose every statement fails.
//
// It exists because the store's error paths are otherwise unreachable: a real
// database does not fail on demand, and these branches decide whether a failure
// surfaces to the worker or is silently swallowed. A swallowed store error here
// means an export that was never produced and never recorded, or — worse — an
// erasure reported as complete that never ran.
type failingClient struct {
	closed *sql.DB
}

var _ database.Client = (*failingClient)(nil)

func (c *failingClient) Reader() database.SQLQueryExecutor { return &failingExecutor{closed: c.closed} }
func (c *failingClient) Writer() database.SQLQueryExecutor { return &failingExecutor{closed: c.closed} }
func (*failingClient) Dialect() dialect.Dialect            { return dialect.SQLite }
func (*failingClient) Close() error                        { return nil }
func (*failingClient) CurrentTime() time.Time              { return baseTime }

// WithTransaction invokes fn with a failing executor and returns whatever fn
// reports, mirroring the real client: the callback runs, its statements fail,
// and the error propagates out through the rollback.
func (c *failingClient) WithTransaction(_ context.Context, fn func(database.SQLQueryExecutor) error) error {
	return fn(&failingExecutor{closed: c.closed})
}

type failingExecutor struct {
	closed *sql.DB
}

var _ database.SQLQueryExecutor = (*failingExecutor)(nil)

func (*failingExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errDatabase
}

func (*failingExecutor) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	return nil, errDatabase
}

func (*failingExecutor) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errDatabase
}

// QueryRowContext delegates to a closed *sql.DB, whose Row reports "database is
// closed" on Scan.
//
// A zero-value &sql.Row{} is not usable here: it has no underlying rows and
// panics rather than reporting an error, so the only way to obtain a Row
// carrying a failure is to ask a real pool that cannot serve it.
func (e *failingExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return e.closed.QueryRowContext(ctx, query, args...)
}

// newFailingClient builds a client whose statements all fail. The closed pool
// exists only to source *sql.Row values that report an error.
func newFailingClient(t *testing.T) *failingClient {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.db"))
	must.NoError(t, err)
	must.NoError(t, db.Close())

	return &failingClient{closed: db}
}

func newFailingStore(t *testing.T) Store {
	t.Helper()

	store, err := NewSQLStore(newFailingClient(t))
	must.NoError(t, err)

	return store
}

// Every one of these asserts the same contract: a store failure is reported,
// not swallowed. The specific error matters less than that something non-nil
// comes back, so the worker treats the request as unfinished and retries it.
func TestSQLStore_PropagatesFailures(T *testing.T) {
	T.Parallel()

	T.Run("Save", func(t *testing.T) {
		t.Parallel()

		store := newFailingStore(t)

		err := store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.Save(t.Context(), q, newRequest("r", RequestExport, testSubject, baseTime))
		})
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("Get", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).Get(t.Context(), "r")
		test.Error(t, err)
	})

	T.Run("List", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).List(t.Context(), testSubject, filtering.DefaultQueryFilter())
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("Transition", func(t *testing.T) {
		t.Parallel()

		store := newFailingStore(t)

		err := store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			_, txErr := store.Transition(t.Context(), q, "r",
				[]Status{StatusAwaitingConfirmation}, StatusInProgress, "op-1", baseTime)

			return txErr
		})
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("CompleteExport", func(t *testing.T) {
		t.Parallel()

		store := newFailingStore(t)

		err := store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.CompleteExport(t.Context(), q, newRequest("r", RequestExport, testSubject, baseTime), baseTime)
		})
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("CompleteErasure", func(t *testing.T) {
		t.Parallel()

		store := newFailingStore(t)

		err := store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.CompleteErasure(t.Context(), q, newRequest("r", RequestErasure, testSubject, baseTime), baseTime)
		})
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("Fail", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).Fail(t.Context(), "r", "boom", baseTime)
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("ExpiringArtifacts", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).ExpiringArtifacts(t.Context(), baseTime, 10)
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("MarkExpired", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, newFailingStore(t).MarkExpired(t.Context(), "r", baseTime), errDatabase)
	})

	T.Run("LapseUnconfirmed", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).LapseUnconfirmed(t.Context(), baseTime, 10)
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("CountOverdue", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).CountOverdue(t.Context(), baseTime)
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("Reap", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).Reap(t.Context(), baseTime, 10)
		test.ErrorIs(t, err, errDatabase)
	})
}

// A limit of zero or less is a no-op rather than an unbounded statement. The
// distinction matters: an unbounded Reap or expiry sweep against a large table
// is a lock held for minutes, and a caller that computed a batch size of zero
// should get nothing rather than everything.
func TestSQLStore_NonPositiveLimits(T *testing.T) {
	T.Parallel()

	T.Run("do no work", func(t *testing.T) {
		t.Parallel()

		store := newFailingStore(t)

		expiring, err := store.ExpiringArtifacts(t.Context(), baseTime, 0)
		test.NoError(t, err)
		test.SliceEmpty(t, expiring)

		lapsed, err := store.LapseUnconfirmed(t.Context(), baseTime, 0)
		test.NoError(t, err)
		test.EqOp(t, int64(0), lapsed)

		reaped, err := store.Reap(t.Context(), baseTime, 0)
		test.NoError(t, err)
		test.EqOp(t, int64(0), reaped)
	})
}

func TestSQLStore_RejectsNilArguments(T *testing.T) {
	T.Parallel()

	T.Run("nil requests and executors", func(t *testing.T) {
		t.Parallel()

		store := newFailingStore(t)

		test.ErrorIs(t, store.Save(t.Context(), &failingExecutor{}, nil), ErrNilRequest)
		test.ErrorIs(t, store.CompleteExport(t.Context(), &failingExecutor{}, nil, baseTime), ErrNilRequest)
		test.ErrorIs(t, store.CompleteErasure(t.Context(), &failingExecutor{}, nil, baseTime), ErrNilRequest)
		test.ErrorIs(t, store.CompleteErasure(t.Context(), nil, nil, baseTime), ErrNilExecutor)
		test.ErrorIs(t, store.CompleteExport(t.Context(), nil, nil, baseTime), ErrNilExecutor)

		_, err := store.Transition(t.Context(), nil, "r", []Status{StatusInProgress}, StatusFailed, "", baseTime)
		test.ErrorIs(t, err, ErrNilExecutor)

		// An empty source-status set would render `status IN ()`, which is a
		// syntax error in every dialect rather than a transition that matches
		// nothing.
		_, err = store.Transition(t.Context(), &failingExecutor{}, "r", nil, StatusFailed, "", baseTime)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})
}

// The sweeper and the runners sit on top of the store, and a store failure has
// to reach their telemetry rather than being mistaken for "nothing to do".
func TestSweeper_PropagatesStoreFailures(T *testing.T) {
	T.Parallel()

	T.Run("a failing store fails the sweep", func(t *testing.T) {
		t.Parallel()

		sweeper, err := NewSweeper(t.Context(), &SweeperConfig{}, newFailingStore(t),
			WithSweeperUploadManager(newMemoryUploader()),
			WithSweeperClock(newStubClock()),
		)
		must.NoError(t, err)

		result, sweepErr := sweeper.Sweep(t.Context())
		must.Error(t, sweepErr)

		// A partial result still comes back, because the chores are unrelated
		// and the caller may want to know which of them got anywhere.
		must.NotNil(t, result)
	})
}
