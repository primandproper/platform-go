package rbac

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/rbac/migrations"

	"github.com/primandproper/primitives-go/authorization"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

const (
	permRead   authorization.Permission = "read.things"
	permWrite  authorization.Permission = "write.things"
	permDelete authorization.Permission = "delete.things"
)

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
//
// openConns is the pool's ceiling, and it is one unless a test says otherwise:
// a pool of one is what makes the SQLite tests deterministic, and it is also a
// pool through which no two transactions can ever be open at once — so a test
// of concurrent writers has to ask for more, or it is a test of the pool.
type testClientConfig struct {
	connectionString string
	openConns        int
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string { return c.connectionString }

// A container reports "ready" from its log line slightly before it accepts TCP
// connections, so the first statement after construction can land on a socket
// that is still closing. These values give IsReady room to ride that out; a
// SQLite client succeeds on the first ping and pays none of it.
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 30 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Second }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return max(c.openConns, 1) }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// testRoles is the shape a real policy takes: a member role, an admin
// inheriting it, a service admin inheriting that, and an unrelated auditor.
func testRoles() []authorization.Role {
	return []authorization.Role{
		{Name: "member", Description: "a member", Permissions: []authorization.Permission{permRead}},
		{Name: "admin", Permissions: []authorization.Permission{permWrite}, Inherits: []string{"member"}},
		{Name: "service_admin", Permissions: []authorization.Permission{permDelete}, Inherits: []string{"admin"}},
		{Name: "auditor", Permissions: []authorization.Permission{permRead}},
	}
}

// newTestClient builds a SQLite-backed client with the policy tables created.
//
// SQLite exercises the real SQL — the recursive CTE, placeholder rendering, the
// batched multi-row inserts — without a container, so the backend's core
// behavior is covered by `make test` rather than only by integration runs.
func newTestClient(t *testing.T) database.Client {
	t.Helper()

	ctx := t.Context()

	client, err := sqlite.NewDatabaseClient(ctx, &testClientConfig{connectionString: filepath.Join(t.TempDir(), "authz.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, DefaultTablePrefix)
	must.NoError(t, err)

	if len(stmts) == 0 {
		t.Fatal("no authorization DDL statements rendered")
	}

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(ctx, stmt)
		must.NoError(t, execErr)
	}

	return client
}

// newTestResolver builds a Resolver over a fresh SQLite database.
func newTestResolver(t *testing.T) (*Resolver, database.Client) {
	t.Helper()

	client := newTestClient(t)

	r, err := NewResolver(
		&Config{Dialect: dialect.SQLite},
		client.Writer(),
		WithLogger(loggingnoop.NewLogger()),
		WithTracerProvider(tracingnoop.NewTracerProvider()),
	)
	must.NoError(t, err)

	return r, client
}

// seed writes roles through a transaction, the way a caller would.
func seed(t *testing.T, r *Resolver, client database.Client, roles ...authorization.Role) {
	t.Helper()

	must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
		return r.Seed(t.Context(), q, roles...)
	}))
}
