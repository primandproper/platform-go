package identity_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"

	"github.com/shoenig/test/must"
)

// TestConformance_SQLite runs every suite against a hand-built identity server
// on a loopback TCP listener over SQLite.
//
// It is the fast subject and it needs no Docker, which is what makes it the one
// that can run on every push without anybody weighing whether it is worth it.
// What it does not prove is anything the database engine decides — SQLite is a
// single writer, it truncates timestamps to the second, and its collation is
// neither of the other two's — so the assertions that turn on those run against
// the real servers in containers_test.go and are no less required for passing
// here.
//
// It runs conformanceall rather than the identity suite alone, deliberately.
// The cross-cutting suites read whichever surfaces a subject mounted, so a
// subject mounting one surface asserts that surface's share of them — which is
// how the anonymous suite's reading of identity's thirty-one RPCs gets executed
// rather than merely compiled.
func TestConformance_SQLite(T *testing.T) {
	T.Parallel()

	db, err := sqlite.NewDatabaseClient(T.Context(),
		&testClientConfig{connectionString: filepath.Join(T.TempDir(), "conformance.db")})
	must.NoError(T, err)
	T.Cleanup(func() { _ = db.Close() })

	runAgainst(T, db, dialect.SQLite)
}

// testClientConfig is the minimum database.ClientConfig a client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 4 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }
