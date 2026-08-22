package webauthncfg

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	"github.com/primandproper/platform-go/v13/authentication/webauthn/database/migrations"
	cachecfg "github.com/primandproper/platform-go/v13/cache/config"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"

	"github.com/shoenig/test/must"
)

// relyingParty is the one part of a Config that is always required, whichever
// provider holds the ceremonies.
func relyingParty() webauthn.Config {
	return webauthn.Config{
		RPID:          "example.com",
		RPDisplayName: "Example",
		RPOrigins:     []string{"https://example.com"},
	}
}

// databaseConfig is a Config for the default provider.
func databaseConfig() *Config {
	return &Config{Provider: ProviderDatabase, RelyingParty: relyingParty()}
}

// cacheConfig is a Config for the cache provider, backed by the memory cache
// that is for tests and single-process services only.
func cacheConfig() *Config {
	return &Config{
		Provider:     ProviderCache,
		RelyingParty: relyingParty(),
		Cache:        cachecfg.Config{Provider: cachecfg.ProviderMemory},
	}
}

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 3 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Second }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// newTestClient builds a SQLite-backed client with the ceremony session table
// created, so the store this assembles is one that can actually hold a
// ceremony.
func newTestClient(tb testing.TB) database.Client {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "webauthn.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, "")
	must.NoError(tb, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr)
	}

	return client
}

// testSession is one ceremony's worth of state, for the assembled stores to
// hold.
func testSession(challenge string) *webauthn.SessionData {
	return &webauthn.SessionData{
		Challenge:      challenge,
		RelyingPartyID: "example.com",
		UserID:         []byte("user-handle"),
		Expires:        time.Now().UTC().Add(time.Minute),
	}
}
