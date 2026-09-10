package mediaregistry

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/mediaregistry/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
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

// The tenants the suite registers objects in. testScope is what a multi-tenant
// consumer passes; otherScope is the neighbor whose rows must never appear in
// testScope's answers.
var (
	testScope  = tenancy.Of("tenant_1")
	otherScope = tenancy.Of("tenant_2")
)

// prefixCounter names a fresh table per subtest. Subtests share one database and
// must not share a table — a scope's object list is global within the table, so
// one test's objects would be another's.
var prefixCounter atomic.Uint64

// storeEnv is one live database plus the dialect to emit SQL for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newStore migrates a uniquely prefixed registry table and returns a Store over
// it.
func (e *storeEnv) newStore(t *testing.T) *SQLStore {
	t.Helper()

	store, err := NewSQLStore(e.client, WithTablePrefix(e.migrate(t)))
	must.NoError(t, err)

	return store
}

// migrate renders a uniquely prefixed registry table and returns the prefix.
func (e *storeEnv) migrate(t *testing.T) string {
	t.Helper()

	prefix := fmt.Sprintf("ur_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return prefix
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real
// SQL — placeholder rendering, the cursor comparison, the two counts riding on
// the rows — without a container.
func newSQLiteEnv(t *testing.T) *storeEnv {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "registry.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// inTx runs fn inside a transaction on the environment's database and reports
// what fn returned.
//
// Both writes take the caller's transaction, so a test that wants an object
// registered opens one — which is what a consumer does. It hands back fn's error
// rather than asserting on it, because a refused write is what half of these
// cases are about and RunInTransaction returns the callback's error unwrapped.
func (e *storeEnv) inTx(tb testing.TB, fn func(tx database.Tx) error) error {
	tb.Helper()

	return e.client.WithTransaction(tb.Context(), fn)
}

// reader is the executor an ordinary read runs on: the client's, outside any
// transaction. The cases about a read that joins a transaction pass the Tx
// instead, and they are in the transaction suite.
func (e *storeEnv) reader() database.SQLQueryExecutor { return e.client.Reader() }

// record registers one object in a transaction of its own and reports both of
// what the write returned.
//
// The transaction is a detail here rather than the subject: these cases are
// about what the write checks and what the reads then see, and a consumer with
// nothing to commit alongside opens exactly this. What a row commits *with* is
// the transaction suite.
//
// The row is captured inside the callback rather than returned through
// RunInTransaction, which carries an error and nothing else. It is left nil on
// the refusals, which is the same answer the write gave.
func (e *storeEnv) record(tb testing.TB, store *SQLStore, scope tenancy.Scope, in ObjectInput) (*Object, error) { //nolint:gocritic // hugeParam: by value on purpose — see ObjectInput, and the call does a round trip
	tb.Helper()

	var recorded *Object

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		recorded, txErr = store.RecordObject(tb.Context(), tx, scope, in)

		return txErr
	})

	return recorded, err
}

// archive soft-deletes one row in a transaction of its own and reports both of
// what the write returned.
func (e *storeEnv) archive(tb testing.TB, store *SQLStore, scope tenancy.Scope, objectID string) (*Object, error) {
	tb.Helper()

	var archived *Object

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		archived, txErr = store.ArchiveObject(tb.Context(), tx, scope, objectID)

		return txErr
	})

	return archived, err
}

// mustRecord and mustArchive are the two above for the cases whose subject is
// what happens after the write rather than the write itself: they fail the test
// on a refusal and hand back the row, so a fixture reads as one line.
//
// They are what the suite reaches for most, because the row is now the only
// place the id, the stamps and the bound scope appear — the argument is not
// written to, so a case that needs the id needs the answer.
func (e *storeEnv) mustRecord(tb testing.TB, store *SQLStore, scope tenancy.Scope, in ObjectInput) *Object { //nolint:gocritic // hugeParam: by value on purpose — see ObjectInput, and the call does a round trip
	tb.Helper()

	recorded, err := e.record(tb, store, scope, in)
	must.NoError(tb, err)
	must.NotNil(tb, recorded)

	return recorded
}

func (e *storeEnv) mustArchive(tb testing.TB, store *SQLStore, scope tenancy.Scope, objectID string) *Object {
	tb.Helper()

	archived, err := e.archive(tb, store, scope, objectID)
	must.NoError(tb, err)
	must.NotNil(tb, archived)

	return archived
}

// newObject is the object the suite records, with the fields a caller has to
// supply and nothing else.
//
// It names no scope: the write's argument is what decides the tenant, and a
// fixture that carried one would be asserting the field the port removed from
// the write path. The cases about a scope-carrying object set it themselves.
func newInput(key, ownerID string) ObjectInput {
	return ObjectInput{
		Key:         key,
		ContentType: "image/png",
		OwnerID:     ownerID,
		Size:        1024,
	}
}
