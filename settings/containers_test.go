package settings

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/settings/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSQLStore_RealServers runs the same behavioral suite SQLite runs, against
// real servers.
//
// It exists because the SQL that only a real server can validate is otherwise
// merely rendered, never executed: numbered placeholders, the upsert's three
// spellings of one conflict clause, the batched read's array binding on Postgres
// against the placeholder expansion on the other two, the partial indexes
// Postgres and SQLite have and MySQL does not, and the filter window compared
// against real temporal types rather than against text.
//
// The upsert is the one this catches. Its conflict clause is spelled three
// different ways for the same meaning and SQLite accepts the form the other two
// reject — so SQLite can never tell us — and it is the statement the whole
// revive-a-cleared-value behavior rests on.
func TestSQLStore_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(_ context.Context, pg *pgtest.Instance) {
			client, err := postgres.NewDatabaseClient(t.Context(),
				&testClientConfig{connectionString: pg.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

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
//
// MySQL is the one with something to prove here beyond parsing: its unique key
// spans four VARCHARs and its enumeration table keys on one, and InnoDB bounds a
// key at 3072 bytes — so a column widened without counting them is a CREATE that
// fails on the server and nowhere else.
func TestMigrations_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(ctx context.Context, pg *pgtest.Instance) {
			stmts, err := migrations.Statements(dialect.Postgres, "ddl_check")
			must.NoError(t, err)

			// Executed twice: every statement is IF NOT EXISTS, so re-running a
			// migration must be a no-op rather than an error.
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

			// Executed twice, as Postgres is. MySQL has no CREATE INDEX IF NOT
			// EXISTS, so every key here is declared inline under its CREATE
			// TABLE IF NOT EXISTS and is skipped along with the table — which
			// only a real server can confirm, since rendering the same DDL
			// again says nothing about what the server does with it.
			for range 2 {
				for _, stmt := range stmts {
					_, execErr := client.Writer().ExecContext(ctx, stmt)
					must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
				}
			}
		})
	})
}

// runWithMySQL starts a MySQL-flavored container and hands the closure a
// database.Client against it.
func runWithMySQL(t *testing.T, fn func(ctx context.Context, client database.Client)) {
	t.Helper()

	runWithMySQLConns(t, 0, fn)
}

// runWithMySQLConns is runWithMySQL with the pool's size named, for the one case
// that holds two transactions open at once. Zero leaves testClientConfig's
// default, which is the single connection every other case wants.
func runWithMySQLConns(t *testing.T, conns int, fn func(ctx context.Context, client database.Client)) {
	t.Helper()

	mysqltest.Run(t, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx,
			&testClientConfig{connectionString: my.ConnectionString, maxOpenConns: conns})
		must.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })

		fn(ctx, client)
	})
}

// lockSettle is how long the narrowing holds its transaction open after the
// value write has been told to go.
//
// It is the test's sensitivity rather than its correctness. The assertions below
// are about the outcome — the narrowing and the set cannot both succeed — and
// those hold however the two transactions interleave. What the wait buys is that
// a store whose lock was removed *fails* this test rather than passing it by
// timing: without a lock the value write reads the old enumeration, admits the
// value and commits well inside this window, and the assertions then see two
// successes. Half a second is generous against a container on the same machine
// and is paid once per dialect, since the locked run spends it waiting.
const lockSettle = 500 * time.Millisecond

// TestSQLStore_StrandedValuesGuarantee_RealServers is the proof of the one rule
// this store owns, under the concurrency it is a rule about.
//
// settings.ErrStrandedValues says an edit that some stored value no longer
// satisfies is refused, and SQLStore.UpdateDefinition implements that by walking
// the live values before it writes. A walk is only a guarantee if nothing can
// write a value between it and the commit, and nothing else in this suite can
// show that: every other case runs one transaction at a time, where a
// check-then-write and a locked one are indistinguishable.
//
// So this one runs two. The narrowing walks, writes and then holds its
// transaction open; the value write, released the moment the walk is done, tries
// to store a value the old enumeration admits and the new one does not. Under
// the lock it waits for the narrowing to commit and is then refused against the
// enumeration that actually won. Without the lock it commits, and the row it
// leaves is the stranded value the guarantee denies.
func TestSQLStore_StrandedValuesGuarantee_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(_ context.Context, pg *pgtest.Instance) {
			client, err := postgres.NewDatabaseClient(t.Context(),
				&testClientConfig{connectionString: pg.ConnectionString, maxOpenConns: concurrentConns})
			must.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			runStrandedValuesRace(t, &storeEnv{client: client, dialect: dialect.Postgres})
		})
	})

	// Through the same helper the rest of this file uses, so it runs against the
	// MariaDB flavor this suite pins rather than stock MySQL — and this is the
	// case that distinction decides. FOR SHARE arrived in MySQL 8.0 and MariaDB
	// has never parsed it, so a share lock written that way would be a syntax
	// error here and nowhere else. LOCK IN SHARE MODE is what gets rendered; see
	// settings/internal/queries' sharedLock.
	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		runWithMySQLConns(t, concurrentConns, func(_ context.Context, client database.Client) {
			runStrandedValuesRace(t, &storeEnv{client: client, dialect: dialect.MySQL})
		})
	})

	// SQLite is skipped rather than covered, and the reason is the engine's
	// storage model rather than a gap in the harness: one writer at a time is
	// what SQLite is, so the two transactions this case interleaves cannot be in
	// flight together there. The second would not race the first — it would wait
	// for it, which is the outcome the lock buys on the other two, delivered by
	// the engine instead of by a clause. That is also why the two statements
	// render there carrying no clause at all; see settings/internal/queries.
	T.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Skip("SQLite is single-writer, so the interleaving this case is about is unreachable")
	})
}

// concurrentConns is what the race needs from the pool: one connection per
// transaction in flight, plus the reads the assertions make afterwards.
const concurrentConns = 4

// racingSubject is the second subject, whose answer the narrowing's walk could
// not have seen however the two transactions were ordered.
var racingSubject = Subject{Type: SubjectUser, ID: "user-2"}

// runStrandedValuesRace interleaves a narrowing and a value write, and asserts
// they cannot both succeed.
func runStrandedValuesRace(t *testing.T, env *storeEnv) {
	t.Helper()

	ctx := t.Context()
	store := env.newStore(t)

	definition := mustCreate(t, env, store, testScope, &Definition{
		Name:        "digest.cadence",
		Description: "how often a digest is sent",
		Kind:        KindString,
		Enumeration: []string{"daily", "never", "weekly"},
	})

	// One subject has already answered, with a value both enumerations admit, so
	// the walk below has a page to walk and still approves the edit. Without it
	// the narrowing would be refusing nothing and waiting on nothing.
	mustSet(t, env, store, testScope, testSubject, definition.Name, "daily")

	narrowed := *definition
	narrowed.Enumeration = []string{"daily", "never"}

	var (
		walked  = make(chan struct{})
		reading = make(chan struct{})

		editErr error
		setErr  error

		wg sync.WaitGroup
	)

	wg.Add(2)

	// The narrowing. It closes walked once its walk and its write are done —
	// which is also once it holds the definition's row — and then stays open
	// long enough for an unlocked value write to have finished.
	go func() {
		defer wg.Done()

		editErr = env.client.WithTransaction(ctx, func(tx database.Tx) error {
			_, err := store.UpdateDefinition(ctx, tx, testScope, &narrowed)

			close(walked)

			if err != nil {
				return err
			}

			<-reading
			time.Sleep(lockSettle)

			return nil
		})
	}()

	// The value write, of a value the enumeration being narrowed away still
	// admits. It is a second subject, so this is a row the walk above could not
	// have seen however it was ordered.
	go func() {
		defer wg.Done()

		setErr = env.client.WithTransaction(ctx, func(tx database.Tx) error {
			<-walked
			close(reading)

			_, err := store.SetValue(ctx, tx, testScope, racingSubject, definition.Name, "weekly")

			return err
		})
	}()

	wg.Wait()

	// The narrowing is the one that wins, because it got there first and held
	// the row until it committed.
	must.NoError(t, editErr)

	// And the set is refused — against the enumeration that won, not the one it
	// read on its way in. That is the whole of what the lock buys: unlocked, this
	// error is nil and the assertion below is what reports the stranded row.
	test.ErrorIs(t, setErr, ErrNotEnumerated)

	// The invariant, stated independently of which transaction won: every live
	// value is one the definition as it now stands admits. It is the thing
	// ErrStrandedValues promises, and it is checked rather than inferred from the
	// two errors above, because an outcome neither of them predicted would still
	// have to satisfy it.
	current, err := store.GetDefinition(ctx, env.reader(), testScope, definition.ID)
	must.NoError(t, err)

	values, err := store.ListValuesForDefinition(ctx, env.reader(), testScope, definition.Name, nil)
	must.NoError(t, err)

	for _, value := range values.Data {
		test.NoError(t, current.admits(value.Raw),
			test.Sprintf("%s %q holds %q, which %q no longer admits",
				value.Subject.Type, value.Subject.ID, value.Raw, current.Name))
	}
}
