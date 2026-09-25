package grants

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/grants/migrations"

	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
	"github.com/primandproper/primitives-go/v2/cryptography/encryption/aes"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// testClientConfig is the minimum database.ClientConfig a client needs.
//
// maxOpenConns is 1 when unset, which is what SQLite gets. A real server's
// client asks for more. With one connection a transaction the suite holds
// open starves every other writer of the pool, so no write could ever reach
// the server while another is in flight, and a race case would pass without
// racing.
type testClientConfig struct {
	connectionString string
	maxOpenConns     int
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return max(1, c.maxOpenConns) }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// The tenants the suite stores grants in. otherScope is the neighbor whose rows
// must never appear in testScope's answers.
var (
	testScope  = tenancy.Of("tenant_1")
	otherScope = tenancy.Of("tenant_2")
)

// testExpiry is the access-token expiry the fixtures carry. It is whole seconds
// because SQLite stores a time at that precision.
var testExpiry = time.Date(2026, time.September, 25, 13, 0, 0, 0, time.UTC)

// prefixCounter names a fresh table per subtest, so subtests sharing one
// database never share rows.
var prefixCounter atomic.Uint64

// keyMaterial is the AES-256 key the test keyring holds. It is a fixed value so
// that two stores built in one test open each other's ciphertext, which is what
// a second replica does.
var keyMaterial = []byte("0123456789abcdef0123456789abcdef")

// newTestEncryptor is a one-key primitives-go keyring, which is what a
// deployment hands the store.
func newTestEncryptor(tb testing.TB) encryption.EncryptorDecryptor {
	tb.Helper()

	cipher, err := aes.NewCipher(keyMaterial)
	must.NoError(tb, err)

	keyring, err := encryption.NewKeyring("k1", []encryption.RingKey{{ID: "k1", Cipher: cipher}})
	must.NoError(tb, err)

	return keyring
}

// storeEnv is one live database plus the dialect to emit SQL for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newStore migrates a uniquely prefixed grant table and returns a Store over it.
func (e *storeEnv) newStore(t *testing.T) *SQLStore {
	t.Helper()

	store, err := NewSQLStore(e.client, newTestEncryptor(t), WithTablePrefix(e.migrate(t)))
	must.NoError(t, err)

	return store
}

// migrate renders a uniquely prefixed grant table and returns the prefix.
func (e *storeEnv) migrate(t *testing.T) string {
	t.Helper()

	prefix := fmt.Sprintf("gr_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return prefix
}

// newSQLiteEnv builds a SQLite-backed environment, which exercises the real SQL
// without a container.
func newSQLiteEnv(t *testing.T) *storeEnv {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "grants.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// inTx runs fn inside a transaction and reports what fn returned, rather than
// asserting on it: a refused write is what half of these cases are about.
func (e *storeEnv) inTx(tb testing.TB, fn func(tx database.Tx) error) error {
	tb.Helper()

	return e.client.WithTransaction(tb.Context(), fn)
}

// reader is the executor an ordinary read runs on.
func (e *storeEnv) reader() database.SQLQueryExecutor { return e.client.Reader() }

// put stores a consent in a transaction of its own.
func (e *storeEnv) put(tb testing.TB, store *SQLStore, scope tenancy.Scope, c *Consent) (*Grant, error) {
	tb.Helper()

	var grant *Grant

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		grant, txErr = store.Put(tb.Context(), tx, scope, c)

		return txErr
	})

	return grant, err
}

// refreshed stores a refresh in a transaction of its own.
func (e *storeEnv) refreshed(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	id, expected string,
	tokens *Tokens,
) (*Grant, error) {
	tb.Helper()

	var grant *Grant

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		grant, txErr = store.Refreshed(tb.Context(), tx, scope, id, expected, tokens)

		return txErr
	})

	return grant, err
}

// revoke revokes a grant in a transaction of its own.
func (e *storeEnv) revoke(tb testing.TB, store *SQLStore, scope tenancy.Scope, id string, reason RevocationReason) (*Grant, error) {
	tb.Helper()

	var grant *Grant

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		grant, txErr = store.Revoke(tb.Context(), tx, scope, id, reason)

		return txErr
	})

	return grant, err
}

// erase deletes a subject's grants in a transaction of its own.
func (e *storeEnv) erase(tb testing.TB, store *SQLStore, scope tenancy.Scope, subject string) (int64, error) {
	tb.Helper()

	var deleted int64

	err := e.inTx(tb, func(tx database.Tx) error {
		var txErr error
		deleted, txErr = store.DeleteForSubject(tb.Context(), tx, scope, subject)

		return txErr
	})

	return deleted, err
}

// mustPut and mustRevoke fail the test on a refusal, so a fixture reads as one
// line.
func (e *storeEnv) mustPut(tb testing.TB, store *SQLStore, scope tenancy.Scope, c *Consent) *Grant {
	tb.Helper()

	grant, err := e.put(tb, store, scope, c)
	must.NoError(tb, err)
	must.NotNil(tb, grant)

	return grant
}

func (e *storeEnv) mustRevoke(tb testing.TB, store *SQLStore, scope tenancy.Scope, id string, reason RevocationReason) *Grant {
	tb.Helper()

	grant, err := e.revoke(tb, store, scope, id, reason)
	must.NoError(tb, err)
	must.NotNil(tb, grant)

	return grant
}

// newConsent is the consent the suite stores, from one subject to one provider.
func newConsent(subject, provider string) *Consent {
	return &Consent{
		Subject:           subject,
		Provider:          provider,
		ProviderAccountID: "owner@example.com",
		GrantedScopes:     []string{"openid", "https://www.googleapis.com/auth/calendar.events"},
		Tokens: Tokens{
			Expiry:       testExpiry,
			AccessToken:  "access-" + subject + "-" + provider,
			RefreshToken: "refresh-" + subject + "-" + provider,
		},
	}
}
