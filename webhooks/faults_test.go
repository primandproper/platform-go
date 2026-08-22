package webhooks

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
// database does not fail on demand, and these branches are the ones that decide
// whether a failure surfaces to the worker or is silently swallowed. A swallowed
// store error in this package means a delivery that was never sent and never
// recorded, which is the worst failure mode it has.
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

// Every one of these asserts the same contract: a store failure is reported, not
// swallowed. The specific error matters less than that something non-nil comes
// back, so that the worker treats the dispatch as unfinished and retries it.
func TestSQLStore_PropagatesFailures(T *testing.T) {
	T.Parallel()

	T.Run("SaveEndpoint", func(t *testing.T) {
		t.Parallel()

		// Not errDatabase: the first statement is the scope check, which reads
		// through QueryRowContext and so reports the closed pool's own error.
		// What matters is that the failure surfaces rather than the upsert
		// running against an unverified row.
		err := newFailingStore(t).SaveEndpoint(t.Context(), &Endpoint{ID: "e", Scope: testScope, Events: []EventType{orderCreated}})
		test.Error(t, err)
	})

	T.Run("GetEndpoint", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).GetEndpoint(t.Context(), testScope, "e")
		test.Error(t, err)
	})

	T.Run("ListEndpoints", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).ListEndpoints(t.Context(), testScope, filtering.DefaultQueryFilter())
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("ArchiveEndpoint", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, newFailingStore(t).ArchiveEndpoint(t.Context(), testScope, "e"), errDatabase)
	})

	T.Run("EndpointsForEvent", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).EndpointsForEvent(t.Context(), newFailingClient(t).Reader(), testScope, "order.created")
		test.ErrorIs(t, err, errDatabase)
	})

	// The one that matters most: Enqueue failing has to abort the caller's
	// transaction, or the state change commits without its events.
	T.Run("Enqueue", func(t *testing.T) {
		t.Parallel()

		err := newFailingStore(t).Enqueue(t.Context(), newFailingClient(t).Writer(),
			&Delivery{ID: "d", Scope: testScope, EventType: "order.created", Payload: testBody},
			[]string{"e"}, baseTime)

		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("Claim", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).Claim(t.Context(), baseTime, 10, baseTime.Add(time.Minute))
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("MarkDelivered", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, newFailingStore(t).MarkDelivered(t.Context(), "d", baseTime), errDatabase)
	})

	T.Run("RecordFailure", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, newFailingStore(t).RecordFailure(t.Context(), "d", 1, baseTime, "boom", false), errDatabase)
	})

	T.Run("RecordAttempt", func(t *testing.T) {
		t.Parallel()

		err := newFailingStore(t).RecordAttempt(t.Context(), &Attempt{DeliveryID: "d", EndpointID: "e"})
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("ListAttempts", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).ListAttempts(t.Context(), testScope, "d", filtering.DefaultQueryFilter())
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("Requeue", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, newFailingStore(t).Requeue(t.Context(), "d", "e", baseTime), errDatabase)
	})

	T.Run("Backlog", func(t *testing.T) {
		t.Parallel()

		_, _, err := newFailingStore(t).Backlog(t.Context())
		test.Error(t, err)
	})

	T.Run("Reap", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingStore(t).Reap(t.Context(), baseTime, 100)
		test.ErrorIs(t, err, errDatabase)
	})
}

// A store failure must not stop the worker: there is no caller to hand the error
// to, and the next cycle retries. What must not happen is a panic or a dispatch
// silently marked done.
func TestWorker_SurvivesStoreFailures(T *testing.T) {
	T.Parallel()

	T.Run("a failure marking delivered leaves the dispatch to redeliver", func(t *testing.T) {
		t.Parallel()

		server := newAcceptingServer(t)

		w := newTestWorker(t, &fakeStore{
			markDelivered: func(context.Context, string, time.Time) error { return errDatabase },
		})

		// The subscriber has the payload but the row still looks pending — the
		// at-least-once window the package documents.
		w.handle(t.Context(), testDispatch(server.URL, 1))
	})

	T.Run("a failure recording an attempt does not stop the delivery", func(t *testing.T) {
		t.Parallel()

		server := newAcceptingServer(t)

		marked := false

		w := newTestWorker(t, &fakeStore{
			recordAttempt: func(context.Context, *Attempt) error { return errDatabase },
			markDelivered: func(context.Context, string, time.Time) error {
				marked = true

				return nil
			},
		})

		w.handle(t.Context(), testDispatch(server.URL, 1))

		// The log entry was lost, but the delivery still counts as delivered —
		// re-sending it because a log write failed would be worse.
		test.True(t, marked)
	})

	T.Run("a failure recording a failure leaves the lease to expire", func(t *testing.T) {
		t.Parallel()

		server := newRefusingServer(t)

		w := newTestWorker(t, &fakeStore{
			recordFailure: func(context.Context, string, int, time.Time, string, bool) error {
				return errDatabase
			},
		})

		// The lease still expires on its own, so the dispatch is retried
		// regardless — just later than intended.
		w.handle(t.Context(), testDispatch(server.URL, 1))
	})
}

func TestTruncateError(T *testing.T) {
	T.Parallel()

	T.Run("nil error renders empty", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", truncateError(nil))
	})

	T.Run("short errors are untouched", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "boom", truncateError(platformerrors.New("boom")))
	})

	// A pathological transport error must not bloat the row it is stored in.
	T.Run("long errors are bounded", func(t *testing.T) {
		t.Parallel()

		long := make([]byte, maxStoredErrorLength*3)
		for i := range long {
			long[i] = 'x'
		}

		test.EqOp(t, maxStoredErrorLength, len(truncateError(platformerrors.New(string(long)))))
	})
}
