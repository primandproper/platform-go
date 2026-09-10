package settings

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/settings/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"

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

// prefixCounter names a fresh table set per subtest. Subtests share one database
// and must not share tables: a definition's name is unique per scope, and a
// suite that reused one table set would have subtests colliding on names they
// each chose freely.
var prefixCounter atomic.Uint64

// The scope most of this suite works in, and a second one every isolation
// assertion reads through. Neither is global, because tenancy.Global() is the
// scope a bug defaults to — a predicate that lost its binding matches it — so a
// suite that worked entirely in it would pass with the scope dropped from every
// statement.
var (
	testScope  = tenancy.Of("acme")
	otherScope = tenancy.Of("other")
)

// testSubject is whose settings most of this suite is about.
var testSubject = Subject{Type: SubjectUser, ID: "user-1"}

// errCompanionWrite stands in for the write a consumer makes beside a setting —
// the audit entry, the outbox event — failing after the setting itself is in the
// transaction.
var errCompanionWrite = platformerrors.New("the companion write failed")

// storeEnv is one live database plus the dialect it speaks.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real SQL
// — placeholder rendering, the upsert's conflict clause, the batched read's
// placeholder expansion, the partial indexes — without a container.
func newSQLiteEnv(tb testing.TB) *storeEnv {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "settings.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// newStore migrates a uniquely prefixed table set and returns a store over it.
func (e *storeEnv) newStore(tb testing.TB, opts ...SQLStoreOption) *SQLStore {
	tb.Helper()

	store, _ := e.newStoreWithPrefix(tb, opts...)

	return store
}

// newStoreWithPrefix is newStore, also handing back the prefix so a test can
// reach the tables directly.
func (e *storeEnv) newStoreWithPrefix(tb testing.TB, opts ...SQLStoreOption) (store *SQLStore, prefix string) {
	tb.Helper()

	prefix = fmt.Sprintf("st_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err = NewSQLStore(e.client, append([]SQLStoreOption{WithTablePrefix(prefix)}, opts...)...)
	must.NoError(tb, err)

	return store, prefix
}

// inTx runs fn inside a transaction on the environment's database and reports
// what fn returned.
//
// Every write in this store takes the caller's transaction, so a test that wants
// a definition or a value written opens one — which is what a consumer does. It
// hands back fn's error rather than asserting on it, because a refused write is
// what half of these cases are about and RunInTransaction returns the callback's
// error unwrapped.
func (e *storeEnv) inTx(tb testing.TB, fn func(tx database.Tx) error) error {
	tb.Helper()

	return e.client.WithTransaction(tb.Context(), fn)
}

// reader is the executor an ordinary read runs on: the client's, outside any
// transaction. The cases about a read that joins a transaction pass the Tx
// instead, and they are in the transactions suite.
func (e *storeEnv) reader() database.SQLQueryExecutor { return e.client.Reader() }

// create writes one definition in a transaction of its own and reports what the
// write returned.
//
// The transaction is a detail here rather than the subject: these cases are about
// what the write checks, and a consumer that has nothing to commit alongside
// opens exactly this. What a definition commits *with* is the transactions suite.
func (e *storeEnv) create(tb testing.TB, store *SQLStore, scope tenancy.Scope, definition *Definition) (*Definition, error) {
	tb.Helper()

	var created *Definition

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		created, txErr = store.CreateDefinition(tb.Context(), tx, scope, definition)

		return txErr
	})

	return created, err
}

// update rewrites one definition in a transaction of its own and reports what
// the write returned.
func (e *storeEnv) update(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	definition *Definition,
) (*Definition, error) {
	tb.Helper()

	var updated *Definition

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		updated, txErr = store.UpdateDefinition(tb.Context(), tx, scope, definition)

		return txErr
	})

	return updated, err
}

// archive retires one definition in a transaction of its own and reports what
// the write returned.
func (e *storeEnv) archive(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	definitionID string,
) error {
	tb.Helper()

	return e.inTx(tb, func(tx database.Tx) error {
		return store.ArchiveDefinition(tb.Context(), tx, scope, definitionID)
	})
}

// set stores one subject's answer in a transaction of its own and reports what
// the write returned.
func (e *storeEnv) set(tb testing.TB, store *SQLStore, scope tenancy.Scope, subject Subject, name, raw string) (*Value, error) {
	tb.Helper()

	var value *Value

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		value, txErr = store.SetValue(tb.Context(), tx, scope, subject, name, raw)

		return txErr
	})

	return value, err
}

// clear takes one subject's answer back in a transaction of its own and reports
// what the write returned.
func (e *storeEnv) clear(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	subject Subject,
	name string,
) (*Value, error) {
	tb.Helper()

	var cleared *Value

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		cleared, txErr = store.ClearValue(tb.Context(), tx, scope, subject, name)

		return txErr
	})

	return cleared, err
}

// erase runs DeleteValuesForSubject in a transaction of its own and returns the
// count, since every caller of it here wants exactly that.
func (e *storeEnv) erase(tb testing.TB, store *SQLStore, scope tenancy.Scope, subject Subject) int64 {
	tb.Helper()

	var deleted int64

	must.NoError(tb, e.inTx(tb, func(tx database.Tx) error {
		var err error
		deleted, err = store.DeleteValuesForSubject(tb.Context(), tx, scope, subject)

		return err
	}))

	return deleted
}

// The definitions this suite writes. Each is a function rather than a var
// because CreateDefinition fills in the id and the creation time, and a shared
// value would carry one subtest's row into the next.

func stringDefinition(name string) *Definition {
	return &Definition{
		Name:        name,
		Description: "how often a digest is sent",
		Kind:        KindString,
		Default:     pointer.To("weekly"),
		Enumeration: []string{"weekly", "daily", "never"},
	}
}

func boolDefinition(name string) *Definition {
	return &Definition{Name: name, Kind: KindBool, Default: pointer.To("true")}
}

func intDefinition(name string) *Definition {
	return &Definition{Name: name, Kind: KindInt}
}

// mustCreate writes a definition and fails the test if it will not go in.
func mustCreate(tb testing.TB, e *storeEnv, store *SQLStore, scope tenancy.Scope, definition *Definition) *Definition {
	tb.Helper()

	created, err := e.create(tb, store, scope, definition)
	must.NoError(tb, err)
	must.NotNil(tb, created)

	return created
}

// mustSet stores a subject's answer and fails the test if it will not go in.
func mustSet(tb testing.TB, e *storeEnv, store *SQLStore, scope tenancy.Scope, subject Subject, name, raw string) *Value {
	tb.Helper()

	value, err := e.set(tb, store, scope, subject, name, raw)
	must.NoError(tb, err)
	must.NotNil(tb, value)

	return value
}

// mustUpdate rewrites a definition and fails the test if the edit is refused.
func mustUpdate(tb testing.TB, e *storeEnv, store *SQLStore, scope tenancy.Scope, definition *Definition) *Definition {
	tb.Helper()

	updated, err := e.update(tb, store, scope, definition)
	must.NoError(tb, err)
	must.NotNil(tb, updated)

	return updated
}

// mustArchive retires a definition and fails the test if it will not retire.
func mustArchive(tb testing.TB, e *storeEnv, store *SQLStore, scope tenancy.Scope, definitionID string) {
	tb.Helper()

	must.NoError(tb, e.archive(tb, store, scope, definitionID))
}

// mustClear takes a subject's answer back and fails the test if there was none.
func mustClear(tb testing.TB, e *storeEnv, store *SQLStore, scope tenancy.Scope, subject Subject, name string) *Value {
	tb.Helper()

	cleared, err := e.clear(tb, store, scope, subject, name)
	must.NoError(tb, err)
	must.NotNil(tb, cleared)

	return cleared
}
