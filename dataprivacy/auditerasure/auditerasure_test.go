package auditerasure

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/audit"
	auditmigrations "github.com/primandproper/platform-go/v13/audit/migrations"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/dataprivacy"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// auditEnv is a live audit log with a Recorder and a Reader over it.
type auditEnv struct {
	client   database.Client
	recorder audit.Recorder
	reader   audit.Reader
}

func newAuditEnv(t *testing.T) *auditEnv {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "audit.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stmts, err := auditmigrations.Statements(dialect.SQLite, audit.DefaultTablePrefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	recorder, err := audit.NewRecorder(dialect.SQLite)
	must.NoError(t, err)

	reader, err := audit.NewReader(client)
	must.NoError(t, err)

	return &auditEnv{client: client, recorder: recorder, reader: reader}
}

// record appends one entry to a scope.
func (e *auditEnv) record(t *testing.T, scope, actorID, resourceID string) {
	t.Helper()

	must.NoError(t, e.client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
		return e.recorder.Record(t.Context(), q, &audit.Entry{
			EventType:    audit.EventUpdated,
			ResourceType: "recipe",
			ResourceID:   resourceID,
			Actor:        audit.Actor{ID: actorID, Type: audit.ActorUser},
			Scope:        scope,
		})
	}))
}

// countEntries reports how many entries survive in a scope.
func (e *auditEnv) countEntries(t *testing.T, scope string) int64 {
	t.Helper()

	var count int64
	must.NoError(t, e.client.Reader().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_log_entries WHERE scope = ?", scope).
		Scan(&count))

	return count
}

// countChains reports how many chain rows survive for a scope.
func (e *auditEnv) countChains(t *testing.T, scope string) int64 {
	t.Helper()

	var count int64
	must.NoError(t, e.client.Reader().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_log_chains WHERE scope = ?", scope).
		Scan(&count))

	return count
}

// erase runs the eraser inside a transaction, as the dataprivacy Worker does.
func (e *auditEnv) erase(t *testing.T, eraser *Eraser, subject dataprivacy.Subject) dataprivacy.ErasureOutcome {
	t.Helper()

	var outcome dataprivacy.ErasureOutcome

	must.NoError(t, e.client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
		var err error
		outcome, err = eraser.Erase(t.Context(), q, subject)

		return err
	}))

	return outcome
}

func TestEraser(T *testing.T) {
	T.Parallel()

	T.Run("deletes the subject's scope entirely", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t)

		env.record(t, "user-1", "user-1", "recipe-1")
		env.record(t, "user-1", "user-1", "recipe-2")

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix)
		must.NoError(t, err)

		outcome := env.erase(t, eraser, dataprivacy.Subject{ID: "user-1"})

		test.EqOp(t, int64(2), outcome.Deleted)
		test.EqOp(t, int64(0), env.countEntries(t, "user-1"))

		// The chain row goes with the entries. Leaving it would leave a scope
		// whose recorded head is ahead of any surviving entry, and a later write
		// would be assigned a position the chain claims is already used.
		test.EqOp(t, int64(0), env.countChains(t, "user-1"))
	})

	T.Run("a surviving scope still verifies", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t)

		env.record(t, "user-1", "user-1", "recipe-1")

		// Three entries in somebody else's tenant, the middle one by the
		// subject. Deleting that middle entry is what would break the chain.
		env.record(t, "account-9", "user-7", "recipe-3")
		env.record(t, "account-9", "user-1", "recipe-4")
		env.record(t, "account-9", "user-7", "recipe-5")

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix)
		must.NoError(t, err)

		env.erase(t, eraser, dataprivacy.Subject{ID: "user-1"})

		result, err := env.reader.Verify(t.Context(), "account-9",
			time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		must.NoError(t, err)

		// This is the whole design. An eraser that deleted or anonymized that
		// middle entry would make every subsequent Verify report tampering, for
		// the rest of that scope's history.
		test.True(t, result.Intact(), test.Sprintf("break: %+v", result.FirstBreak))
		test.EqOp(t, 3, result.Checked)
	})

	T.Run("reports what it could not remove", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t)

		env.record(t, "user-1", "user-1", "recipe-1")

		// One where the subject acted inside somebody else's tenant, one where
		// they were the thing acted on.
		env.record(t, "account-9", "user-1", "recipe-2")
		env.record(t, "account-9", "user-7", "user-1")

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix)
		must.NoError(t, err)

		outcome := env.erase(t, eraser, dataprivacy.Subject{ID: "user-1"})

		test.EqOp(t, int64(1), outcome.Deleted)

		// Two entries elsewhere still name the subject: one where they acted,
		// one where they were the resource. Both are reported with a basis
		// rather than silently kept.
		must.MapLen(t, 1, outcome.Retained)
		test.StrContains(t, outcome.Retained["entries"], "2 ")
		test.StrContains(t, outcome.Retained["entries"], "legitimate interest")
	})

	T.Run("nothing to retain reports no retentions", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t)

		env.record(t, "user-1", "user-1", "recipe-1")

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix)
		must.NoError(t, err)

		outcome := env.erase(t, eraser, dataprivacy.Subject{ID: "user-1"})

		test.EqOp(t, int64(1), outcome.Deleted)
		test.MapEmpty(t, outcome.Retained)
	})

	T.Run("a custom scope resolver decides what is deletable", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t)

		env.record(t, "tenant-a", "user-1", "recipe-1")
		env.record(t, "tenant-b", "user-1", "recipe-2")

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix,
			WithScopeResolver(func(_ context.Context, s dataprivacy.Subject) ([]string, error) {
				return []string{"tenant-a", "tenant-b"}, nil
			}))
		must.NoError(t, err)

		outcome := env.erase(t, eraser, dataprivacy.Subject{ID: "user-1"})

		test.EqOp(t, int64(2), outcome.Deleted)
		test.MapEmpty(t, outcome.Retained)
	})

	T.Run("resolving no scopes deletes nothing", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t)

		env.record(t, "user-1", "user-1", "recipe-1")

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix,
			WithScopeResolver(func(context.Context, dataprivacy.Subject) ([]string, error) {
				return nil, nil
			}))
		must.NoError(t, err)

		outcome := env.erase(t, eraser, dataprivacy.Subject{ID: "user-1"})

		// Legitimate: it means nothing is deletable and everything is retained.
		test.EqOp(t, int64(0), outcome.Deleted)
		must.MapLen(t, 1, outcome.Retained)
		test.EqOp(t, int64(1), env.countEntries(t, "user-1"))
	})

	T.Run("the retention basis is overridable", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t)

		env.record(t, "account-9", "user-1", "recipe-1")

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix,
			WithRetentionBasis("kept under Article 17(3)(b)"))
		must.NoError(t, err)

		outcome := env.erase(t, eraser, dataprivacy.Subject{ID: "user-1"})

		test.StrContains(t, outcome.Retained["entries"], "Article 17(3)(b)")
	})

	T.Run("refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix)
		must.NoError(t, err)

		_, err = eraser.Erase(t.Context(), nil, dataprivacy.Subject{ID: "user-1"})
		test.Error(t, err)
	})
}

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := New(dialect.Dialect("oracle"), audit.DefaultTablePrefix)
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects a prefix that is not an identifier fragment", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"drop table;--", "has space", "1leading"} {
			_, err := New(dialect.SQLite, prefix)
			test.ErrorIs(t, err, ErrInvalidTablePrefix, test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("accepts an empty prefix", func(t *testing.T) {
		t.Parallel()

		// The audit package's own prefix rule allows it; the rendered names are
		// then bare "entries" and "chains", which are still legal identifiers.
		_, err := New(dialect.SQLite, "")
		test.NoError(t, err)
	})

	T.Run("WithTablePrefix overrides the constructor's prefix", func(t *testing.T) {
		t.Parallel()

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix, WithTablePrefix("custom"))
		must.NoError(t, err)

		test.EqOp(t, "custom_audit_log_entries", eraser.entries)
		test.EqOp(t, "custom_audit_log_chains", eraser.chains)
	})

	T.Run("rejects a prefix an option renders illegal", func(t *testing.T) {
		t.Parallel()

		// The option runs before the identifier check, so a prefix smuggled in
		// this way is caught on the same terms as the constructor's.
		_, err := New(dialect.SQLite, audit.DefaultTablePrefix, WithTablePrefix("bad name "))
		test.ErrorIs(t, err, ErrInvalidTablePrefix)
	})

	T.Run("propagates a scope resolver error", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t)

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix,
			WithScopeResolver(func(context.Context, dataprivacy.Subject) ([]string, error) {
				return nil, platformerrors.New("tenant directory is down")
			}))
		must.NoError(t, err)

		err = env.client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			_, eraseErr := eraser.Erase(t.Context(), q, dataprivacy.Subject{ID: "user-1"})

			return eraseErr
		})

		must.Error(t, err)
		test.StrContains(t, err.Error(), "tenant directory is down")
	})
}

// errDatabase is what the fault-injecting executor returns.
var errDatabase = platformerrors.New("database is on fire")

// failingExecutor fails every statement.
//
// The eraser's error paths are otherwise unreachable — a real database does not
// fail on demand — and they decide whether a half-applied audit deletion is
// reported or silently swallowed. A swallowed error here means an erasure that
// reports success having left the subject's audit scope in place.
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

// QueryRowContext delegates to a closed pool, whose Row reports an error on
// Scan. A zero-value &sql.Row{} panics instead of reporting.
func (e *failingExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return e.closed.QueryRowContext(ctx, query, args...)
}

func newClosedPool(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.db"))
	must.NoError(t, err)
	must.NoError(t, db.Close())

	return db
}

// countOnlyExecutor lets the deletes through against a live database but fails
// the retained-count read, isolating the second half of Erase.
type countOnlyExecutor struct {
	database.SQLQueryExecutor

	closed *sql.DB
}

func (e *countOnlyExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return e.closed.QueryRowContext(ctx, query, args...)
}

func TestEraser_PropagatesFailures(T *testing.T) {
	T.Parallel()

	T.Run("a failing delete is reported", func(t *testing.T) {
		t.Parallel()

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix)
		must.NoError(t, err)

		_, err = eraser.Erase(t.Context(), &failingExecutor{closed: newClosedPool(t)},
			dataprivacy.Subject{ID: "user-1"})

		must.ErrorIs(t, err, errDatabase)
		test.StrContains(t, err.Error(), "deleting audit entries")
	})

	T.Run("a failing retained count is reported", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t)
		env.record(t, "user-1", "user-1", "recipe-1")

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix)
		must.NoError(t, err)

		closed := newClosedPool(t)

		err = env.client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			_, eraseErr := eraser.Erase(t.Context(),
				&countOnlyExecutor{SQLQueryExecutor: q, closed: closed},
				dataprivacy.Subject{ID: "user-1"})

			return eraseErr
		})

		must.Error(t, err)
		test.StrContains(t, err.Error(), "counting retained audit entries")
	})

	T.Run("no scopes still counts what is retained", func(t *testing.T) {
		t.Parallel()

		eraser, err := New(dialect.SQLite, audit.DefaultTablePrefix,
			WithScopeResolver(func(context.Context, dataprivacy.Subject) ([]string, error) {
				return nil, nil
			}))
		must.NoError(t, err)

		// The delete is skipped entirely, so the only statement is the count —
		// and its failure still has to surface.
		_, err = eraser.Erase(t.Context(), &failingExecutor{closed: newClosedPool(t)},
			dataprivacy.Subject{ID: "user-1"})

		must.Error(t, err)
		test.StrContains(t, err.Error(), "counting retained audit entries")
	})
}
