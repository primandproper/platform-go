package oauth2clients

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// The two registries this suite works in. Two rather than one because half of
// what these tests assert is that a read in one cannot see the other's rows —
// and because the one read that deliberately crosses them has to be shown
// crossing them.
var (
	testScope  = tenancy.Of("acct_1")
	otherScope = tenancy.Of("acct_2")
)

// The two people. A registration belongs to a registry and may belong to
// somebody inside it, and the owner is what the self-service page keys on.
const (
	testOwner  = "user_1"
	otherOwner = "user_2"
)

// testRedirect is a redirect URI oauth2server.ValidateRedirectURI accepts, so
// that a test about scoping is not also a test about URI validation.
const testRedirect = "https://example.test/callback"

// testClientConfig is the minimal database.ClientConfig these tests dial with.
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

// prefixCounter names a fresh table per subtest. Subtests share one database and
// must not share a table: client_id is globally unique, so one subtest's
// registration would collide with another's the moment two of them minted the
// same fixture.
var prefixCounter atomic.Uint64

// storeEnv is one live database plus the dialect its statements are generated
// for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real SQL
// — placeholder rendering, the partial indexes, the unique index on client_id —
// without a container.
func newSQLiteEnv(tb testing.TB) *storeEnv {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "oauth2clients.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// newStore migrates a uniquely prefixed table and returns a store over it.
func (e *storeEnv) newStore(tb testing.TB, opts ...SQLStoreOption) *SQLStore {
	tb.Helper()

	prefix := fmt.Sprintf("oc_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := NewSQLStore(e.client, append([]SQLStoreOption{WithTablePrefix(prefix)}, opts...)...)
	must.NoError(tb, err)

	return store
}

// inTx runs fn inside a transaction and reports what fn returned.
//
// Every write in this store takes the caller's transaction, so a test that wants
// a registration written opens one — which is what a consumer does. It hands
// back fn's error rather than asserting on it, because a refused write is what
// several of these cases are about.
func (e *storeEnv) inTx(tb testing.TB, fn func(tx database.Tx) error) error {
	tb.Helper()

	return e.client.WithTransaction(tb.Context(), fn)
}

// reader is the executor an ordinary read runs on: the client's, outside any
// transaction.
func (e *storeEnv) reader() database.SQLQueryExecutor { return e.client.Reader() }

// newClient builds a registration fixture. The two identifiers are minted per
// call because client_id carries a unique index across every registry.
func newClient(scope tenancy.Scope, owner string) *Client {
	id := identifiers.New()

	return &Client{
		Scope:         scope,
		BelongsToUser: owner,
		ID:            id,
		ClientID:      "cid_" + id,
		SecretHash:    hashSecret("secret_" + id),
		Name:          "test client",
		RedirectURIs:  []string{testRedirect},
	}
}

// create writes one registration in a transaction of its own and reports what
// the write answered with.
//
// The row comes back rather than being read off the argument, because the write
// no longer touches it: the creation time the database assigned is on what the
// store returned and nowhere else.
func (e *storeEnv) create(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	client *Client,
) (*Client, error) {
	tb.Helper()

	var written *Client

	err := e.inTx(tb, func(tx database.Tx) error {
		created, createErr := store.CreateClient(tb.Context(), tx, scope, client)
		written = created

		return createErr
	})

	return written, err
}

// createErr is create for the cases that are about the refusal, so a subtest
// asserting on a sentinel does not have to name a row it knows will be nil.
func (e *storeEnv) createErr(tb testing.TB, store *SQLStore, scope tenancy.Scope, client *Client) error {
	tb.Helper()

	written, err := e.create(tb, store, scope, client)

	// The row comes back only alongside a nil error, which is the property every
	// refusal in this suite is also asserting.
	if err != nil {
		must.Nil(tb, written, must.Sprint("a refused create answered with a row"))
	}

	return err
}

// update revises one registration in a transaction of its own and reports what
// the write answered with.
func (e *storeEnv) update(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	id string,
	input *UpdateInput,
) (*Client, error) {
	tb.Helper()

	var revised *Client

	err := e.inTx(tb, func(tx database.Tx) error {
		written, updateErr := store.UpdateClient(tb.Context(), tx, scope, id, input)
		revised = written

		return updateErr
	})

	return revised, err
}

// archive withdraws one registration and reports only the refusal, for the cases
// that are about which rows a write reaches rather than about what it hands back.
func (e *storeEnv) archive(tb testing.TB, store *SQLStore, scope tenancy.Scope, id string) error {
	tb.Helper()

	return e.inTx(tb, func(tx database.Tx) error {
		withdrawn, archiveErr := store.ArchiveClient(tb.Context(), tx, scope, id)
		if archiveErr != nil {
			must.Nil(tb, withdrawn, must.Sprint("a refused archive answered with a row"))
		}

		return archiveErr
	})
}

// seed writes a registration and fails the test if it could not be written. What
// it hands back is the stored row, so a fixture carries the creation time and
// the scope the write settled.
func (e *storeEnv) seed(tb testing.TB, store *SQLStore, scope tenancy.Scope, owner string) *Client {
	tb.Helper()

	client, err := e.create(tb, store, scope, newClient(scope, owner))
	must.NoError(tb, err)

	return client
}
