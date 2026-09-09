package audit

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/primandproper/platform-go/v14/audit/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/migrate"
	"github.com/primandproper/primitives-go/database/mysql"
	"github.com/primandproper/primitives-go/database/postgres"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	"github.com/primandproper/primitives-go/tenancy"
	"github.com/primandproper/primitives-go/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// defaultMySQLImage pins the MariaDB flavor this suite exercises; mysqltest's
// default is stock MySQL.
const defaultMySQLImage = "mariadb:11"

// prefixCounter names a fresh pair of tables per subtest. Subtests share one
// container, so they must not share tables — the chain is global to a scope
// within a table, and one test's entries would be another's.
var prefixCounter atomic.Uint64

// dialectEnv is one live database plus the dialect its SQL should be emitted
// for.
type dialectEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newPrefix creates a uniquely named pair of audit tables and returns the
// prefix they share.
func (e *dialectEnv) newPrefix(t *testing.T) string {
	t.Helper()

	prefix := fmt.Sprintf("audit%d", prefixCounter.Add(1))
	applyMigrations(t, e.client, e.dialect, prefix)

	return prefix
}

// recorder builds a Recorder bound to the supplied prefix.
func (e *dialectEnv) recorder(t *testing.T, c *stubClock, prefix string, opts ...RecorderOption) Recorder {
	t.Helper()

	r, err := NewRecorder(e.dialect,
		append([]RecorderOption{WithRecorderClock(c), WithRecorderTablePrefix(prefix)}, opts...)...)
	must.NoError(t, err)

	return r
}

// reader builds a Reader bound to the supplied prefix.
func (e *dialectEnv) reader(t *testing.T, prefix string) Reader {
	t.Helper()

	r, err := NewReader(e.client, WithReaderTablePrefix(prefix))
	must.NoError(t, err)

	return r
}

// prune runs one retention batch against the supplied prefix, the way a
// retention.Sweeper would: inside a transaction it owns, with a row budget.
func (e *dialectEnv) prune(t *testing.T, c *stubClock, prefix string, retention time.Duration) int64 {
	t.Helper()

	target := PruneTarget{TablePrefix: prefix}
	cutoff := c.Now().UTC().Add(-retention)

	var removed int64
	must.NoError(t, e.client.WithTransaction(t.Context(), func(q database.Tx) error {
		var err error
		removed, err = target.Sweep(t.Context(), q, e.dialect, cutoff, 100)

		return err
	}))

	return removed
}

// erasure builds an Erasure bound to the supplied prefix, the way
// dataprivacy/auditerasure builds the one it wraps.
func (e *dialectEnv) erasure(t *testing.T, prefix string) *Erasure {
	t.Helper()

	er, err := NewErasure(e.dialect, WithErasureTablePrefix(prefix))
	must.NoError(t, err)

	return er
}

// runDialectSuite is the behavioral contract every dialect owes. SQLite is
// covered by the in-process tests; this exists so the SQL only a real server
// can validate — numbered placeholders, FOR UPDATE, ON CONFLICT against
// INSERT IGNORE, native timestamp and blob handling, and above all whether a
// microsecond timestamp survives the round trip the hash chain depends on — is
// actually executed rather than merely rendered.
func runDialectSuite(t *testing.T, env *dialectEnv) {
	t.Helper()

	t.Run("records and verifies a chain", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		prefix := env.newPrefix(t)
		recorder := env.recorder(t, c, prefix)
		reader := env.reader(t, prefix)

		first, second := entryFor("acct_1", "r1"), entryFor("acct_1", "r2")

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, first, second)
		}))

		result, err := reader.Verify(t.Context(), tenancy.Of("acct_1"), time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
		test.EqOp(t, 2, result.Checked)
	})

	t.Run("round-trips the timestamp the digest is taken over", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		prefix := env.newPrefix(t)
		recorder := env.recorder(t, c, prefix)
		reader := env.reader(t, prefix)

		entry := entryFor("acct_1", "r1")
		entry.RecordedAt = time.Date(2026, time.July, 31, 12, 0, 0, 123456789, time.UTC)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, entry)
		}))

		// The truncation at the write site is what makes this hold: Postgres and
		// MySQL keep microseconds, and a timestamp that changed on the way back
		// would make every entry read as tampered.
		read, err := reader.Get(t.Context(), entry.ID)
		must.NoError(t, err)
		test.EqOp(t, entry.RecordedAt, read.RecordedAt)

		result, err := reader.Verify(t.Context(), tenancy.Of("acct_1"), time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
	})

	t.Run("rolls back with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		prefix := env.newPrefix(t)
		recorder := env.recorder(t, c, prefix)

		boom := fmt.Errorf("caller work failed")

		err := env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			if recordErr := recorder.Record(t.Context(), q, entryFor("acct_1", "r1")); recordErr != nil {
				return recordErr
			}

			return boom
		})
		test.ErrorIs(t, err, boom)

		test.EqOp(t, 0, countRows(t, env.client, prefix+"_audit_log_entries", "1=1"))
	})

	t.Run("detects tampering", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		prefix := env.newPrefix(t)
		recorder := env.recorder(t, c, prefix)
		reader := env.reader(t, prefix)

		first, second := entryFor("acct_1", "r1"), entryFor("acct_1", "r2")

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, first, second)
		}))

		_, err := env.client.Writer().ExecContext(t.Context(),
			fmt.Sprintf("UPDATE %s_audit_log_entries SET actor_id = %s WHERE id = %s",
				prefix, env.dialect.Placeholder(1), env.dialect.Placeholder(2)),
			"somebody_else", second.ID)
		must.NoError(t, err)

		result, err := reader.Verify(t.Context(), tenancy.Of("acct_1"), time.Time{}, time.Time{})
		must.NoError(t, err)
		must.False(t, result.Intact())
		test.EqOp(t, BreakContentAltered, result.FirstBreak.Reason)
	})

	t.Run("refuses a forked chain", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		prefix := env.newPrefix(t)
		recorder := env.recorder(t, c, prefix)

		entry := entryFor("acct_1", "r1")

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, entry)
		}))

		// The unique index on (scope, seq) is what makes a fork unrepresentable
		// rather than merely detectable, and only a real server proves the index
		// was actually created.
		_, err := env.client.Writer().ExecContext(t.Context(),
			fmt.Sprintf(
				"INSERT INTO %s_audit_log_entries "+
					"(id, seq, scope, recorded_at, event_type, resource_type, resource_id, "+
					"actor_id, actor_type, actor_ip, change_set, metadata, prev_hash, hash) "+
					"VALUES (%s, 0, 'acct_1', %s, 'updated', 'recipe', 'r', 'u', 'user', '', NULL, NULL, '', 'deadbeef')",
				prefix, env.dialect.Placeholder(1), env.dialect.Placeholder(2),
			),
			"fork", entry.RecordedAt)
		test.Error(t, err)
	})

	t.Run("prunes past retention and stays verifiable", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		prefix := env.newPrefix(t)
		recorder := env.recorder(t, c, prefix)
		reader := env.reader(t, prefix)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, entryFor("acct_1", "r1"))
		}))

		c.advance(2 * time.Hour)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, entryFor("acct_1", "r2"))
		}))

		// Exercises the CASE-expression prune bounds and the keyset scope page
		// on a real server, which are the statements most likely to differ
		// between dialects.
		test.EqOp(t, int64(1), env.prune(t, c, prefix, time.Hour))

		result, err := reader.Verify(t.Context(), tenancy.Of("acct_1"), time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
		test.EqOp(t, 1, result.Checked)
	})

	t.Run("erases whole scopes and counts the mentions the chain keeps", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		prefix := env.newPrefix(t)
		recorder := env.recorder(t, c, prefix)
		reader := env.reader(t, prefix)
		erasure := env.erasure(t, prefix)

		// The subject as the thing acted on rather than the actor, which is the
		// second arm of the count's disjunction, and one entry that names them
		// in neither column.
		actedOn := entryFor("acct_9", "user_1")
		actedOn.Actor = Actor{ID: "user_2", Type: ActorUser}

		unrelated := entryFor("acct_9", "r4")
		unrelated.Actor = Actor{ID: "user_2", Type: ActorUser}

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, entryFor("user_1", "r1"), entryFor("user_1", "r2"))
		}))
		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, entryFor("user_1_devices", "d1"))
		}))
		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, entryFor("acct_9", "r3"), actedOn, unrelated)
		}))

		// Two scopes in one statement. The bound set is `= ANY($1::text[])` on
		// Postgres and an expanded `IN (?, ?)` on MySQL, and neither rendering
		// runs anywhere the in-process SQLite suite can reach it.
		var deleted, remaining int64

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			var err error
			if deleted, err = erasure.DeleteScopes(t.Context(), q, []string{"user_1", "user_1_devices"}); err != nil {
				return err
			}

			// Counted in the same transaction and after the deletion, so the
			// three entries just removed are not also reported as retained.
			remaining, err = erasure.CountMentions(t.Context(), q, "user_1")

			return err
		}))

		test.EqOp(t, int64(3), deleted)
		test.EqOp(t, int64(2), remaining)

		// Entries and chain rows go together, for both scopes.
		test.EqOp(t, 0, countRows(t, env.client, prefix+"_audit_log_entries", "scope = 'user_1'"))
		test.EqOp(t, 0, countRows(t, env.client, prefix+"_audit_log_chains", "scope = 'user_1'"))
		test.EqOp(t, 0, countRows(t, env.client, prefix+"_audit_log_entries", "scope = 'user_1_devices'"))
		test.EqOp(t, 0, countRows(t, env.client, prefix+"_audit_log_chains", "scope = 'user_1_devices'"))

		// Somebody else's tenant is untouched, chain row and all, and still
		// verifies across the deletion.
		test.EqOp(t, 3, countRows(t, env.client, prefix+"_audit_log_entries", "scope = 'acct_9'"))
		test.EqOp(t, 1, countRows(t, env.client, prefix+"_audit_log_chains", "scope = 'acct_9'"))

		result, err := reader.Verify(t.Context(), tenancy.Of("acct_9"), time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact(), test.Sprintf("break: %+v", result.FirstBreak))
		test.EqOp(t, 3, result.Checked)

		// The delete order is what makes an erased scope writable again. Had the
		// chain row outlived its entries, this write would take a position past
		// a head that is no longer there, and the verification below would
		// report the hole as tampering.
		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, entryFor("user_1", "r5"))
		}))

		result, err = reader.Verify(t.Context(), tenancy.Of("user_1"), time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact(), test.Sprintf("break: %+v", result.FirstBreak))
		test.EqOp(t, 1, result.Checked)
	})

	t.Run("serializes concurrent writers into one scope", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		prefix := env.newPrefix(t)
		recorder := env.recorder(t, c, prefix)
		reader := env.reader(t, prefix)

		// The chain row's lock is what makes this work. Without it both
		// transactions would read the same head, compute the same position, and
		// one would fail on the unique index.
		const writers = 8

		errs := make(chan error, writers)
		for i := range writers {
			go func() {
				errs <- env.client.WithTransaction(t.Context(), func(q database.Tx) error {
					return recorder.Record(t.Context(), q, entryFor("acct_1", fmt.Sprintf("r%d", i)))
				})
			}()
		}

		for range writers {
			must.NoError(t, <-errs)
		}

		result, err := reader.Verify(t.Context(), tenancy.Of("acct_1"), time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
		test.EqOp(t, writers, result.Checked)
	})

	t.Run("creates a chain for a new scope without racing", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		prefix := env.newPrefix(t)
		recorder := env.recorder(t, c, prefix)

		// The first write to a scope inserts its chain row, and two of them
		// arriving together must not take a caller's transaction down on a
		// primary key conflict — hence the ON CONFLICT / INSERT IGNORE clause.
		const writers = 4

		errs := make(chan error, writers)
		for range writers {
			go func() {
				errs <- env.client.WithTransaction(t.Context(), func(q database.Tx) error {
					return recorder.Record(t.Context(), q, entryFor("brand_new_scope", "r"))
				})
			}()
		}

		for range writers {
			must.NoError(t, <-errs)
		}

		test.EqOp(t, writers, countRows(t, env.client, prefix+"_audit_log_entries", "scope = 'brand_new_scope'"))
	})

	t.Run("refuses an update once the append-only trigger is installed", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		prefix := env.newPrefix(t)
		applyAppendOnly(t, env.client, env.dialect, prefix)

		recorder := env.recorder(t, c, prefix)
		entry := entryFor("acct_1", "r1")

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, entry)
		}))

		_, err := env.client.Writer().ExecContext(t.Context(),
			fmt.Sprintf("UPDATE %s_audit_log_entries SET actor_id = 'somebody_else' WHERE id = %s",
				prefix, env.dialect.Placeholder(1)),
			entry.ID)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "append-only")
	})

	t.Run("pages and filters", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		prefix := env.newPrefix(t)
		recorder := env.recorder(t, c, prefix)
		reader := env.reader(t, prefix)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q,
				entryFor("acct_1", "r1"),
				entryFor("acct_1", "r2"),
				entryFor("acct_2", "r3"),
			)
		}))

		scope := "acct_1"

		listed, err := reader.List(t.Context(), &Query{Scope: &scope}, nil)
		must.NoError(t, err)
		test.SliceLen(t, 2, listed.Data)
		test.EqOp(t, uint64(2), listed.TotalCount)
	})
}

func TestAudit_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, &dialectEnv{dialect: dialect.Postgres, client: client})
	}, pgtest.WithMaxOpenConns(32))
}

// runWithMySQL boots a MySQL container via mysqltest and hands its closure a
// database.Client against it.
func runWithMySQL(tb testing.TB, fn func(ctx context.Context, client database.Client)) {
	tb.Helper()

	mysqltest.Run(tb, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString})
		must.NoError(tb, err)
		tb.Cleanup(func() { _ = client.Close() })

		fn(ctx, client)
	},
		mysqltest.WithImage(defaultMySQLImage),
		mysqltest.WithCredentials("audittest", "audittest", "audittest"),
	)
}

func TestAudit_MySQL(T *testing.T) {
	T.Parallel()

	runWithMySQL(T, func(_ context.Context, client database.Client) {
		runDialectSuite(T, &dialectEnv{dialect: dialect.MySQL, client: client})
	})
}

// migrateTables creates the audit tables the way a consumer would: by handing
// the generated DDL to database/migrate as a migration at a version of their
// choosing.
func migrateTables(t *testing.T, client database.Client, d dialect.Dialect, prefix string) {
	t.Helper()

	ddl, err := migrations.SQL(d, prefix)
	must.NoError(t, err)

	m, err := migrate.New(d, fstest.MapFS{},
		migrate.WithGeneratedMigration(1, "create_audit_tables", ddl),
		migrate.WithLogger(loggingnoop.NewLogger()),
	)
	must.NoError(t, err)

	raw, ok := client.(database.RawAccess)
	must.True(t, ok, must.Sprint("client does not expose RawAccess"))

	must.NoError(t, m.Migrate(t.Context(), raw.WriteDB()))

	// Idempotent, so a second replica booting against the same database is a
	// no-op rather than a failure.
	must.NoError(t, m.Migrate(t.Context(), raw.WriteDB()))
}

// TestAudit_MigratorIntegration_Containers is the end-to-end wiring a consumer
// actually writes: hand migrations.SQL to database/migrate as a generated
// migration, let the normal migration run create the tables, then use them. It
// runs against real servers because the point is that the generated migration
// survives goose's own statement splitting on each dialect.
func TestAudit_MigratorIntegration_Containers(T *testing.T) {
	T.Parallel()

	assertUsable := func(t *testing.T, client database.Client, d dialect.Dialect, prefix string) {
		t.Helper()

		recorder, err := NewRecorder(d, WithRecorderClock(newStubClock()), WithRecorderTablePrefix(prefix))
		must.NoError(t, err)

		reader, err := NewReader(client, WithReaderTablePrefix(prefix))
		must.NoError(t, err)

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
			return recorder.Record(t.Context(), q, entryFor("acct_1", "r1"))
		}))

		result, err := reader.Verify(t.Context(), tenancy.Of("acct_1"), time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
	}

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(_ context.Context, pg *pgtest.Instance) {
			client, err := postgres.NewDatabaseClient(t.Context(), &testClientConfig{connectionString: pg.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			const prefix = "migrated_audit"

			migrateTables(t, client, dialect.Postgres, prefix)
			assertUsable(t, client, dialect.Postgres, prefix)
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(_ context.Context, client database.Client) {
			const prefix = "migrated_audit"

			migrateTables(t, client, dialect.MySQL, prefix)
			assertUsable(t, client, dialect.MySQL, prefix)
		})
	})
}

// TestAudit_Migrations_RealServers proves the shipped DDL is accepted verbatim
// by each server, independent of whether the suite above exercises every column.
func TestAudit_Migrations_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(ctx context.Context, pg *pgtest.Instance) {
			stmts, err := migrations.Statements(dialect.Postgres, "ddl_check")
			must.NoError(t, err)

			// Re-running must be a no-op: every statement is IF NOT EXISTS.
			for range 2 {
				for _, stmt := range stmts {
					_, execErr := pg.DB.ExecContext(ctx, stmt)
					must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
				}
			}
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(ctx context.Context, client database.Client) {
			stmts, err := migrations.Statements(dialect.MySQL, "ddl_check")
			must.NoError(t, err)

			// Executed twice: CREATE TABLE IF NOT EXISTS carries the inline KEY
			// clauses with it, so a second run must not trip over them.
			for range 2 {
				for _, stmt := range stmts {
					_, execErr := client.Writer().ExecContext(ctx, stmt)
					must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
				}
			}
		})
	})

	T.Run("statements carry no unrendered placeholder", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := migrations.Statements(d, "ddl_check")
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.False(t, strings.Contains(stmt, "{{"))
			}
		}
	})
}
