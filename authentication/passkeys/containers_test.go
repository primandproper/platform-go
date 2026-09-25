package passkeys

import (
	"context"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passkeys/internal/passkeysdb"
	"github.com/primandproper/platform-go/v14/authentication/passkeys/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSQLStore_RealServers runs the same behavioral suite SQLite runs, against
// real servers.
//
// It exists because the SQL that only a real server can validate is otherwise
// merely rendered, never executed: numbered versus positional placeholders, the
// bytes columns against three drivers' handling of them, the server-assigned
// created_at the create reads back inside its own transaction, and — the one
// this table turns on — a unique index whose predicate is spelled two different
// ways.
func TestSQLStore_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		runWithPostgres(t, func(_ context.Context, client database.Client) {
			runStoreSuite(t, &storeEnv{client: client, dialect: dialect.Postgres})
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(_ context.Context, client database.Client) {
			runStoreSuite(t, &storeEnv{client: client, dialect: dialect.MySQL})
		})
	})
}

// TestMigrations_RealServers proves the shipped DDL is accepted verbatim by each
// server, independent of whether the store then exercises every column.
func TestMigrations_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		runWithPostgres(t, func(ctx context.Context, client database.Client) {
			stmts, err := migrations.Statements(dialect.Postgres, "ddl_check")
			must.NoError(t, err)

			// Executed twice: every statement is IF NOT EXISTS, so re-running a
			// migration must be a no-op rather than an error.
			for range 2 {
				for _, stmt := range stmts {
					_, execErr := client.Writer().ExecContext(ctx, stmt)
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

			// Executed twice, as Postgres is. MySQL has no CREATE INDEX IF NOT
			// EXISTS, so every key here is declared inline under its CREATE
			// TABLE IF NOT EXISTS and is skipped along with the table.
			//
			// This is also the only place the generated column is checked
			// against a server at all. Rendering it says nothing about whether
			// the engine will accept a unique key over a VIRTUAL column, and
			// the index key length is the other half: a 255-character scope and
			// a 1,023-byte credential id have to stay inside InnoDB's
			// 3,072-byte limit, and it fails here rather than in a consumer's
			// migration if they do not.
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

// TestUniqueness_RealServers proves the index is what actually guarantees one
// live row per credential, rather than the read CreateCredential runs first.
//
// The pre-check turns the ordinary collision into ErrCredentialRegistered; two
// registrations racing for one credential reach the index instead, and the loser
// must be refused by the server. Only a real server enforces that — SQLite in
// this suite runs one connection at a time — and on MySQL the enforcement is a
// generated column rather than a partial index, so what is being proved there is
// that the substitution actually works.
func TestUniqueness_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		runWithPostgres(t, func(_ context.Context, client database.Client) {
			assertIndexEnforcesLiveUniqueness(t, &storeEnv{client: client, dialect: dialect.Postgres})
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(_ context.Context, client database.Client) {
			assertIndexEnforcesLiveUniqueness(t, &storeEnv{client: client, dialect: dialect.MySQL})
		})
	})
}

// assertIndexEnforcesLiveUniqueness writes past the pre-check, by issuing the
// INSERT the store would issue directly — and then proves the same index lets
// the write through once the first row is archived.
func assertIndexEnforcesLiveUniqueness(t *testing.T, env *storeEnv) {
	t.Helper()

	store := env.newStore(t)

	credentialID := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	first := env.mustCreate(t, store, testScope, newCredential("user_1", credentialID))

	duplicate := newCredential("user_2", credentialID)

	// On the environment's own writer rather than through the store, which no
	// longer holds one: the point is to reach the index without the check the
	// store runs first.
	err := store.q.CreateCredential(t.Context(), env.client.Writer(),
		createCredentialParams(testScope, identifiers.New(), emptyTransports, duplicate))
	must.Error(t, err)

	// The index is partial, so archiving the first row frees the credential —
	// and this is the half a plain UNIQUE would fail, silently, on the dialect
	// that has no partial index.
	env.mustArchive(t, store, testScope, first.ID, "user_1")

	err = store.q.CreateCredential(t.Context(), env.client.Writer(),
		createCredentialParams(testScope, identifiers.New(), emptyTransports, duplicate))
	must.NoError(t, err)
}

// TestRecordUse_CountsTheSameOnEveryEngine is the case behind the caveat the
// store's doc names.
//
// MySQL's :execrows counts rows *changed* rather than matched, so a replayed
// assertion carrying the counter the row already holds would report zero there
// and one everywhere else — and the store reads zero as "no such credential",
// which would make a login fail on one engine and succeed on the other two. The
// conventional stamp in the SET list is what keeps the count meaning one thing,
// and this is where that is checked against a server rather than against the
// rendered text.
func TestRecordUse_CountsTheSameOnEveryEngine(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		runWithPostgres(t, func(_ context.Context, client database.Client) {
			assertRepeatedUseIsAWrite(t, &storeEnv{client: client, dialect: dialect.Postgres})
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(_ context.Context, client database.Client) {
			assertRepeatedUseIsAWrite(t, &storeEnv{client: client, dialect: dialect.MySQL})
		})
	})

	T.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		assertRepeatedUseIsAWrite(t, newSQLiteEnv(t))
	})
}

// assertRepeatedUseIsAWrite records the same counter twice and requires both to
// be reported as writes.
func assertRepeatedUseIsAWrite(t *testing.T, env *storeEnv) {
	t.Helper()

	store := env.newStore(t)

	written := env.mustCreate(t, store, testScope, newCredential("user_1", []byte{0x01, 0x02}))

	env.mustRecordUse(t, store, testScope, written.ID, 9)
	used := env.mustRecordUse(t, store, testScope, written.ID, 9)

	test.EqOp(t, uint32(9), used.SignCount)
}

// TestArchivedReadBack_RealServers proves the read-back MySQL's lack of
// RETURNING forces is the same answer everywhere.
func TestArchivedReadBack_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		runWithPostgres(t, func(_ context.Context, client database.Client) {
			assertArchiveReadsItselfBack(t, &storeEnv{client: client, dialect: dialect.Postgres})
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQL(t, func(_ context.Context, client database.Client) {
			assertArchiveReadsItselfBack(t, &storeEnv{client: client, dialect: dialect.MySQL})
		})
	})
}

// assertArchiveReadsItselfBack requires the archive's answer to carry the stamp
// the server wrote, read on the transaction that wrote it.
func assertArchiveReadsItselfBack(t *testing.T, env *storeEnv) {
	t.Helper()

	store := env.newStore(t)

	written := env.mustCreate(t, store, testScope, newCredential("user_1", []byte{0x11, 0x22}))

	var archived *Credential

	err := env.inTx(t, func(tx database.Tx) error {
		var txErr error
		archived, txErr = store.ArchiveCredentialForUser(t.Context(), tx, testScope, written.ID, "user_1")
		if txErr != nil {
			return txErr
		}

		// Read back inside the transaction that hid it: every caller-facing
		// read filters archived_at IS NULL, so this row is reachable from
		// nowhere else.
		_, readErr := store.q.GetArchivedCredential(t.Context(), tx, passkeysdb.GetArchivedCredentialParams{
			ID:    written.ID,
			Scope: testScope,
		})

		return readErr
	})
	must.NoError(t, err)
	must.NotNil(t, archived)
	must.NotNil(t, archived.ArchivedAt)
}

// runWithPostgres starts a Postgres container and hands the closure a
// database.Client against it.
func runWithPostgres(t *testing.T, fn func(ctx context.Context, client database.Client)) {
	t.Helper()

	pgtest.Run(t, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx,
			&testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })

		fn(ctx, client)
	})
}

// runWithMySQL starts a MySQL-flavored container and hands the closure a
// database.Client against it.
func runWithMySQL(t *testing.T, fn func(ctx context.Context, client database.Client)) {
	t.Helper()

	mysqltest.Run(t, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx,
			&testClientConfig{connectionString: my.ConnectionString})
		must.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })

		fn(ctx, client)
	})
}
