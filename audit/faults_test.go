package audit

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v10/database"
	"github.com/primandproper/platform-go/v10/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/filtering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errDatabase is what the fault-injecting client returns from every statement.
var errDatabase = platformerrors.New("database is on fire")

// failingClient is a database.Client whose every statement fails.
//
// These branches are otherwise unreachable — a real database does not fail on
// demand — and they are the ones that decide whether a failure surfaces to the
// caller or is quietly swallowed. Swallowing one here is the worst thing this
// package can do: a Record that reports success without writing leaves a
// committed change with no record of who made it, which is precisely the state
// the package exists to make impossible.
type failingClient struct {
	closed *sql.DB
}

var _ database.Client = (*failingClient)(nil)

func (c *failingClient) Reader() database.SQLQueryExecutor { return &failingExecutor{closed: c.closed} }
func (c *failingClient) Writer() database.SQLQueryExecutor { return &failingExecutor{closed: c.closed} }
func (*failingClient) Dialect() dialect.Dialect            { return dialect.SQLite }
func (*failingClient) Close() error                        { return nil }
func (*failingClient) CurrentTime() time.Time              { return time.Time{} }

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

func TestRecorder_PropagatesFailures(T *testing.T) {
	T.Parallel()

	T.Run("reports a chain head that cannot be read", func(t *testing.T) {
		t.Parallel()

		client := newFailingClient(t)
		r := newTestRecorder(t, newStubClock())

		err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return r.Record(t.Context(), q, entryFor("acct_1", "recipe_1"))
		})
		test.Error(t, err)
	})

	T.Run("reports an insert that fails", func(t *testing.T) {
		t.Parallel()

		// The chain row read succeeds against a real database; the entry insert
		// is what fails, via an executor that only breaks on Exec.
		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return r.Record(t.Context(), q, entryFor("acct_1", "seed"))
		}))

		err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return r.Record(t.Context(), &execFailingExecutor{SQLQueryExecutor: q}, entryFor("acct_1", "recipe_1"))
		})
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("reports a value that cannot be encoded", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		entry := entryFor("acct_1", "recipe_1")
		// A channel has no JSON encoding, so the canonical form cannot be built
		// — and an entry whose digest cannot be computed must not be written.
		entry.Changes = map[string]Change{"broken": {New: make(chan int)}}

		err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return r.Record(t.Context(), q, entry)
		})
		test.Error(t, err)

		test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "1=1"))
	})

	T.Run("reports a value that cannot be hashed for redaction", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock(),
			WithRedaction("recipe", Redaction{Hash: []string{"old", "new", "meta"}}))

		for _, changes := range []map[string]Change{
			{"old": {Old: make(chan int)}},
			{"new": {New: make(chan int)}},
		} {
			entry := entryFor("acct_1", "recipe_1")
			entry.Changes = changes

			err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
				return r.Record(t.Context(), q, entry)
			})
			test.Error(t, err)
		}
	})
}

// execFailingExecutor passes reads through to a working executor and fails
// every write, so the statement under test is the insert rather than the chain
// read that precedes it.
type execFailingExecutor struct {
	database.SQLQueryExecutor
}

func (*execFailingExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errDatabase
}

func TestReader_PropagatesFailures(T *testing.T) {
	T.Parallel()

	newFailingReader := func(t *testing.T) Reader {
		t.Helper()

		r, err := NewReader(newFailingClient(t))
		must.NoError(t, err)

		return r
	}

	T.Run("Get", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingReader(t).Get(t.Context(), "entry_1")
		test.Error(t, err)
	})

	T.Run("List", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingReader(t).List(t.Context(), nil, filtering.DefaultQueryFilter())
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("List reports a count that fails after the page succeeded", func(t *testing.T) {
		t.Parallel()

		// The page read goes through, the total does not: a partial answer must
		// not be returned as if it were whole.
		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		record(t, client, recorder, entryFor("acct_1", "recipe_1"))

		reader, err := NewReader(&countFailingClient{Client: client})
		must.NoError(t, err)

		_, err = reader.List(t.Context(), nil, nil)
		test.Error(t, err)
	})

	T.Run("Verify", func(t *testing.T) {
		t.Parallel()

		_, err := newFailingReader(t).Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		test.ErrorIs(t, err, errDatabase)
	})

	T.Run("Verify reports a chain row that cannot be read", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		record(t, client, recorder, entryFor("acct_1", "recipe_1"))

		// The chain row is gone but its table is not, so the read fails rather
		// than reporting no rows — the case where "never pruned" cannot be
		// assumed.
		exec(t, client, "DROP TABLE "+"audit_log_chains")

		_, err := newTestReader(t, client).Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		test.Error(t, err)
	})

	T.Run("Verify reports an anchor lookup that fails", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)

		record(t, client, recorder, entryFor("acct_1", "r1"))

		// Recorded a day later, so the window below genuinely excludes the first
		// entry and the walk has to go fetch it to anchor against.
		c.advance(24 * time.Hour)

		second := entryFor("acct_1", "r2")
		record(t, client, recorder, second)

		// A range starting mid-chain has to fetch the entry before it; that read
		// failing is not the same as the entry being absent, and must not be
		// reported as tampering.
		// One read — the chain row — goes through; the anchor lookup after it
		// does not.
		remaining := 1

		reader, err := NewReader(&rowFailingClient{Client: client, remaining: &remaining})
		must.NoError(t, err)

		_, err = reader.Verify(t.Context(), "acct_1", second.RecordedAt, time.Time{})
		test.Error(t, err)
	})
}

// countFailingClient serves the page read from a real database and fails the
// count that follows it.
type countFailingClient struct {
	database.Client
}

func (c *countFailingClient) Reader() database.SQLQueryExecutor {
	return &countFailingExecutor{SQLQueryExecutor: c.Client.Reader()}
}

type countFailingExecutor struct {
	database.SQLQueryExecutor
}

func (e *countFailingExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return e.SQLQueryExecutor.QueryRowContext(ctx, "SELECT nonexistent_column", args...)
}

// rowFailingClient serves the first failAfter single-row reads normally and
// breaks every one after that.
//
// The counter lives on the client rather than the executor because Reader() is
// called once per statement, so per-executor state would reset before it ever
// reached zero.
type rowFailingClient struct {
	database.Client
	remaining *int
}

func (c *rowFailingClient) Reader() database.SQLQueryExecutor {
	return &rowFailingExecutor{SQLQueryExecutor: c.Client.Reader(), remaining: c.remaining}
}

type rowFailingExecutor struct {
	database.SQLQueryExecutor
	remaining *int
}

func (e *rowFailingExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if *e.remaining > 0 {
		*e.remaining--

		return e.SQLQueryExecutor.QueryRowContext(ctx, query, args...)
	}

	return e.SQLQueryExecutor.QueryRowContext(ctx, "SELECT nonexistent_column", args...)
}

func TestScanRows_ReportsIterationFailure(T *testing.T) {
	T.Parallel()

	T.Run("surfaces an error from the scan callback", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(T)
		recorder := newTestRecorder(T, newStubClock())
		record(T, client, recorder, entryFor("acct_1", "recipe_1"))

		rows, err := client.Reader().QueryContext(t.Context(),
			"SELECT id FROM "+"audit_log_entries")
		must.NoError(t, err)

		boom := platformerrors.New("callback failed")
		test.ErrorIs(t, scanRows(rows, func() error { return boom }), boom)
	})

	T.Run("surfaces a row that cannot be scanned into an entry", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		entry := entryFor("acct_1", "recipe_1")
		record(t, client, recorder, entry)

		// Field blobs that are not JSON: a decode failure has to be reported
		// rather than yielding an entry with silently empty changes.
		exec(t, client,
			"UPDATE audit_log_entries SET change_set = ? WHERE id = ?", []byte("not json"), entry.ID)

		_, err := newTestReader(t, client).Get(t.Context(), entry.ID)
		test.Error(t, err)

		exec(t, client,
			"UPDATE audit_log_entries SET change_set = NULL, metadata = ? WHERE id = ?",
			[]byte("not json"), entry.ID)

		_, err = newTestReader(t, client).List(t.Context(), nil, nil)
		test.Error(t, err)
	})
}
