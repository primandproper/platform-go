package webhooks

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/tenancy"
	"github.com/primandproper/platform-go/v13/webhooks/migrations"

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

// The fixtures the delivery tests sign and verify against. They live here
// rather than beside a signing test because the scheme itself moved to
// cryptography/requestsigning; what is left in this package is a caller of it.
var (
	signingTime = time.Unix(1753900000, 0).UTC()
	testBody    = []byte(`{"id":"abc","amount":42}`)
)

// The scopes the suite registers endpoints and dispatches deliveries in.
// testScope is what a multi-tenant consumer passes; otherScope is the neighbor
// whose rows must never appear in testScope's answers.
var (
	testScope  = tenancy.Of("acct_1")
	otherScope = tenancy.Of("acct_2")
)

// prefixCounter names a fresh set of tables per subtest. Subtests may share one
// database, and they must not share tables — the claim predicate is global to
// the dispatches table, so one test's backlog would be another's.
var prefixCounter atomic.Uint64

// storeEnv is one live database plus the dialect to emit SQL for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newStore migrates a uniquely prefixed set of webhook tables and returns a
// Store over them.
func (e *storeEnv) newStore(t *testing.T) Store {
	t.Helper()

	store, err := NewSQLStore(e.client, WithTablePrefix(e.migrate(t)))
	must.NoError(t, err)

	return store
}

// migrate renders a uniquely prefixed set of webhook tables and returns the
// prefix, for a test that wants to build the store over them itself.
func (e *storeEnv) migrate(t *testing.T) string {
	t.Helper()

	prefix := fmt.Sprintf("wh_%d", prefixCounter.Add(1))

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
// SQL — placeholder rendering, the ordering predicate, the lease arithmetic,
// the join projections — without a container.
func newSQLiteEnv(t *testing.T) *storeEnv {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "webhooks.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// registerEndpoint saves an endpoint in testScope, subscribed to the given
// events.
func registerEndpoint(t *testing.T, store Store, id string, events ...EventType) *Endpoint {
	t.Helper()

	return registerScopedEndpoint(t, store, testScope, id, events...)
}

// registerScopedEndpoint saves an endpoint in an explicit scope, for the cases
// that need two tenants in one table.
func registerScopedEndpoint(t *testing.T, store Store, scope tenancy.Scope, id string, events ...EventType) *Endpoint {
	t.Helper()

	endpoint := &Endpoint{
		ID:          id,
		Scope:       scope,
		URL:         "https://93.184.216.34/hooks/" + id,
		ContentType: DefaultContentType,
		Secret:      Secret{Current: []byte("secret-" + id)},
		Events:      events,
	}

	must.NoError(t, store.SaveEndpoint(t.Context(), endpoint))

	return endpoint
}

// dispatchTo writes a delivery and fans it out to the named endpoints, the way
// Dispatch would.
func dispatchTo(t *testing.T, env *storeEnv, store Store, delivery *Delivery, at time.Time, endpointIDs ...string) *Delivery {
	t.Helper()

	if delivery.ID == "" {
		delivery.ID = identifiers.New()
	}

	if delivery.Scope.Validate() != nil {
		delivery.Scope = testScope
	}

	must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
		return store.Enqueue(t.Context(), q, delivery, endpointIDs, at)
	}))

	return delivery
}

// claimAll claims with a generous limit and a lease that will not expire during
// the test.
func claimAll(t *testing.T, store Store, now time.Time) []ClaimedDispatch {
	t.Helper()

	claimed, err := store.Claim(t.Context(), now, 100, now.Add(time.Minute))
	must.NoError(t, err)

	return claimed
}

// endpointsFor resolves testScope's fan-out set through a transaction, as
// Dispatch does.
func endpointsFor(t *testing.T, env *storeEnv, store Store, eventType EventType) []*Endpoint {
	t.Helper()

	return scopedEndpointsFor(t, env, store, testScope, eventType)
}

// scopedEndpointsFor resolves one scope's fan-out set through a transaction.
func scopedEndpointsFor(t *testing.T, env *storeEnv, store Store, scope tenancy.Scope, eventType EventType) []*Endpoint {
	t.Helper()

	var endpoints []*Endpoint

	must.NoError(t, env.client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
		var err error
		endpoints, err = store.EndpointsForEvent(t.Context(), q, scope, eventType)

		return err
	}))

	return endpoints
}

// dispatchIDs projects the dispatch IDs of a claimed batch.
func dispatchIDs(claimed []ClaimedDispatch) []string {
	ids := make([]string, 0, len(claimed))
	for i := range claimed {
		ids = append(ids, claimed[i].ID)
	}

	return ids
}

// endpointIDsOf projects the endpoint IDs of a claimed batch.
func endpointIDsOf(claimed []ClaimedDispatch) []string {
	ids := make([]string, 0, len(claimed))
	for i := range claimed {
		ids = append(ids, claimed[i].EndpointID)
	}

	return ids
}

// baseTime is the instant the store suite works relative to.
var baseTime = time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)

// ctxFor is a small convenience so the suite reads the same in both harnesses.
func ctxFor(t *testing.T) context.Context {
	t.Helper()

	return t.Context()
}
