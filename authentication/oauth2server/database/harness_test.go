package database

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server/database/migrations"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

// testClientConfig is the minimum database.ClientConfig a client needs.
type testClientConfig struct {
	connectionString string

	// maxOpenConns is zero for SQLite, which serializes on the file anyway, and
	// set for the container runs — where the cases about two requests racing for
	// one credential need two connections to race on.
	maxOpenConns int
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string { return c.connectionString }

func (c *testClientConfig) GetMaxPingAttempts() uint64       { return 30 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration { return time.Second }
func (c *testClientConfig) GetMaxIdleConns() int             { return 2 }

func (c *testClientConfig) GetMaxOpenConns() int {
	if c.maxOpenConns > 0 {
		return c.maxOpenConns
	}

	return 1
}

func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// newTestClient builds a SQLite-backed client with this package's four tables
// created.
//
// SQLite exercises the real SQL — the placeholder rendering, the insert-ignore
// clause, the guarded UPDATE that makes a credential single-use, the
// transaction a family revocation runs in — without a container, so the store's
// core behavior is covered by `make test` rather than only by integration runs.
func newTestClient(t *testing.T) database.Client {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "oauth2.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	createTables(t, client, dialect.SQLite, DefaultTablePrefix)

	return client
}

// createTables runs the shipped DDL against a client.
func createTables(t *testing.T, client database.Client, d dialect.Dialect, prefix string) {
	t.Helper()

	stmts, err := migrations.Statements(d, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}
}

// newTestStore builds a store over a fresh SQLite database.
func newTestStore(t *testing.T, opts ...Option) *Store {
	t.Helper()

	store, err := NewStore(&Config{}, newTestClient(t), append([]Option{
		WithLogger(loggingnoop.NewLogger()),
		WithTracerProvider(tracingnoop.NewTracerProvider()),
	}, opts...)...)
	must.NoError(t, err)

	return store
}
