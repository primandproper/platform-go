package dataprivacy

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	_ "github.com/primandproper/primitives-go/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"

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
func (c *failingClient) WithTransaction(_ context.Context, fn func(database.Tx) error) error {
	return fn(database.NewTxForTesting(&failingExecutor{closed: c.closed}))
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

		err := store.WithTransaction(t.Context(), func(q database.Tx) error {
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

	T.Run("Confirm", func(t *testing.T) {
		t.Parallel()

		store := newFailingStore(t)

		err := store.WithTransaction(t.Context(), func(q database.Tx) error {
			_, txErr := store.Confirm(t.Context(), q, "r", "op-1")

			return txErr
		})
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("Cancel", func(t *testing.T) {
		t.Parallel()

		store := newFailingStore(t)

		err := store.WithTransaction(t.Context(), func(q database.Tx) error {
			_, txErr := store.Cancel(t.Context(), q, "r", StatusInProgress, baseTime)

			return txErr
		})
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("CompleteExport", func(t *testing.T) {
		t.Parallel()

		store := newFailingStore(t)

		err := store.WithTransaction(t.Context(), func(q database.Tx) error {
			return store.CompleteExport(t.Context(), q, newRequest("r", RequestExport, testSubject, baseTime), baseTime)
		})
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("CompleteErasure", func(t *testing.T) {
		t.Parallel()

		store := newFailingStore(t)

		err := store.WithTransaction(t.Context(), func(q database.Tx) error {
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

		// A scalar count reaches the driver as a Row rather than as Rows, so
		// what comes back is the closed pool's error rather than this file's
		// sentinel — see failingExecutor.QueryRowContext. What matters is the
		// same either way: the gauge reports the failure instead of publishing
		// a zero that reads as "nobody is owed anything".
		_, err := newFailingStore(t).CountOverdue(t.Context(), baseTime)
		test.Error(t, err)
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

		test.ErrorIs(t, store.Save(t.Context(), database.NewTxForTesting(&failingExecutor{}), nil), ErrNilRequest)
		test.ErrorIs(t, store.CompleteExport(t.Context(), database.NewTxForTesting(&failingExecutor{}), nil, baseTime), ErrNilRequest)
		test.ErrorIs(t, store.CompleteErasure(t.Context(), database.NewTxForTesting(&failingExecutor{}), nil, baseTime), ErrNilRequest)
		test.ErrorIs(t, store.CompleteErasure(t.Context(), nil, nil, baseTime), ErrNilExecutor)
		test.ErrorIs(t, store.CompleteExport(t.Context(), nil, nil, baseTime), ErrNilExecutor)

		_, err := store.Cancel(t.Context(), nil, "r", StatusInProgress, baseTime)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.Confirm(t.Context(), nil, "r", "op-1")
		test.ErrorIs(t, err, ErrNilExecutor)

		// A status nothing writes matches no row, so without the check the call
		// would report the request as not cancellable rather than the argument
		// as wrong — and the two are answered very differently by whoever reads
		// the error.
		_, err = store.Cancel(t.Context(), database.NewTxForTesting(&failingExecutor{}), "r", Status("nonsense"), baseTime)
		test.ErrorIs(t, err, ErrUnknownStatus)
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
