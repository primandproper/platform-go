package passkeys

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passkeys/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

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

// The tenants the suite registers passkeys in. testScope is what a multi-tenant
// consumer passes; otherScope is the neighbor whose rows must never appear in
// testScope's answers.
var (
	testScope  = tenancy.Of("tenant_1")
	otherScope = tenancy.Of("tenant_2")
)

// testUsedAt is the instant the suite records an assertion at. It is a fixed
// value rather than time.Now, because RecordUse binds the caller's clock and the
// cases below assert on what came back.
var testUsedAt = time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)

// prefixCounter names a fresh table per subtest. Subtests share one database and
// must not share a table — a scope's credential list is global within the table,
// so one test's rows would be another's.
var prefixCounter atomic.Uint64

// storeEnv is one live database plus the dialect to emit SQL for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newStore migrates a uniquely prefixed credential table and returns a Store over
// it.
func (e *storeEnv) newStore(t *testing.T) *SQLStore {
	t.Helper()

	store, err := NewSQLStore(e.client, WithTablePrefix(e.migrate(t)))
	must.NoError(t, err)

	return store
}

// migrate renders a uniquely prefixed credential table and returns the prefix.
func (e *storeEnv) migrate(t *testing.T) string {
	t.Helper()

	prefix := fmt.Sprintf("pk_%d", prefixCounter.Add(1))

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
// SQL — placeholder rendering, the partial unique index, the archived predicate
// every single-row read carries — without a container.
func newSQLiteEnv(t *testing.T) *storeEnv {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "passkeys.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// inTx runs fn inside a transaction on the environment's database and reports
// what fn returned.
//
// Every write takes the caller's transaction, so a test that wants a passkey
// registered opens one — which is what a consumer does. It hands back fn's error
// rather than asserting on it, because a refused write is what half of these
// cases are about.
func (e *storeEnv) inTx(tb testing.TB, fn func(tx database.Tx) error) error {
	tb.Helper()

	return e.client.WithTransaction(tb.Context(), fn)
}

// reader is the executor an ordinary read runs on: the client's, outside any
// transaction.
func (e *storeEnv) reader() database.SQLQueryExecutor { return e.client.Reader() }

// create registers one passkey in a transaction of its own and reports both of
// what the write returned.
func (e *storeEnv) create(tb testing.TB, store *SQLStore, scope tenancy.Scope, c *Credential) (*Credential, error) {
	tb.Helper()

	var written *Credential

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		written, txErr = store.CreateCredential(tb.Context(), tx, scope, c)

		return txErr
	})

	return written, err
}

// recordUse writes a sign count back in a transaction of its own.
func (e *storeEnv) recordUse(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	rowID string,
	signCount uint32,
) (*Credential, error) {
	tb.Helper()

	var used *Credential

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		used, txErr = store.RecordUse(tb.Context(), tx, scope, rowID, signCount, testUsedAt)

		return txErr
	})

	return used, err
}

// archive revokes one passkey in a transaction of its own.
func (e *storeEnv) archive(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	rowID, userID string,
) (*Credential, error) {
	tb.Helper()

	var archived *Credential

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		archived, txErr = store.ArchiveCredentialForUser(tb.Context(), tx, scope, rowID, userID)

		return txErr
	})

	return archived, err
}

// erase destroys every passkey a subject holds, in a transaction of its own.
func (e *storeEnv) erase(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	userID string,
) (int64, error) {
	tb.Helper()

	var deleted int64

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		deleted, txErr = store.DeleteCredentialsForUser(tb.Context(), tx, scope, userID)

		return txErr
	})

	return deleted, err
}

// mustCreate, mustRecordUse and mustArchive are the three above for the cases
// whose subject is what happens after the write rather than the write itself:
// they fail the test on a refusal and hand back the row, so a fixture reads as
// one line.
func (e *storeEnv) mustCreate(tb testing.TB, store *SQLStore, scope tenancy.Scope, c *Credential) *Credential {
	tb.Helper()

	written, err := e.create(tb, store, scope, c)
	must.NoError(tb, err)
	must.NotNil(tb, written)

	return written
}

func (e *storeEnv) mustRecordUse(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	rowID string,
	signCount uint32,
) *Credential {
	tb.Helper()

	used, err := e.recordUse(tb, store, scope, rowID, signCount)
	must.NoError(tb, err)
	must.NotNil(tb, used)

	return used
}

func (e *storeEnv) mustArchive(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	rowID, userID string,
) *Credential {
	tb.Helper()

	archived, err := e.archive(tb, store, scope, rowID, userID)
	must.NoError(tb, err)
	must.NotNil(tb, archived)

	return archived
}

// newCredential is the passkey the suite registers, with the fields a caller has
// to supply and nothing else.
//
// It names no scope: the write's argument is what decides the tenant, and a
// fixture that carried one would be asserting the field the convention keeps out
// of the write path. The cases about a scope-carrying credential set it
// themselves.
func newCredential(userID string, credentialID []byte) *Credential {
	return &Credential{
		BelongsToUser: userID,
		CredentialID:  credentialID,
		PublicKey:     []byte{0x01, 0x02, 0x03},
		Transports:    []string{"internal", "hybrid"},
		FriendlyName:  "Phone",
	}
}
