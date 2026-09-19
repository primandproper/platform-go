package entitlements

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/metering"
	meteringmigrations "github.com/primandproper/platform-go/v14/metering/migrations"

	"github.com/primandproper/primitives-go/v2/cache/memory"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// meteringPrefix keeps each test's metering tables to itself, as metering's own
// suite does, so the tables one subtest migrates are not the tables another reads.
var meteringPrefix atomic.Int64

// sqliteClientConfig is the minimum database.ClientConfig a SQLite client needs.
type sqliteClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*sqliteClientConfig)(nil)

func (c *sqliteClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *sqliteClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *sqliteClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *sqliteClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *sqliteClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *sqliteClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *sqliteClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// countingExecutor counts the statements run on it.
//
// It implements database.SQLQueryExecutor rather than embedding one: an embedded
// executor would carry any method this one forgot to override straight through to
// the database uncounted, which is a count that is silently short.
type countingExecutor struct {
	q database.SQLQueryExecutor

	statements atomic.Int64
}

var _ database.SQLQueryExecutor = (*countingExecutor)(nil)

func (e *countingExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	e.statements.Add(1)

	return e.q.ExecContext(ctx, query, args...)
}

func (e *countingExecutor) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	e.statements.Add(1)

	return e.q.PrepareContext(ctx, query)
}

func (e *countingExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	e.statements.Add(1)

	return e.q.QueryContext(ctx, query, args...)
}

func (e *countingExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	e.statements.Add(1)

	return e.q.QueryRowContext(ctx, query, args...)
}

// newDatabasePlans builds a PlanSource that reads the account's plan out of a
// table, on the executor it is given.
//
// A database-backed PlanSource is what a deployment actually wires — this module
// ships billing/plans, which reads the account's current subscriptions through
// billing.SubscriptionStore.ListCurrentSubscriptions. What matters to the count
// below is only that answering PlanFor costs a statement, so this is the cheapest
// thing that costs one rather than a second copy of that store's wiring.
func newDatabasePlans(t *testing.T, q database.SQLQueryExecutor, table, plan string) PlanSource {
	t.Helper()

	_, err := q.ExecContext(t.Context(),
		fmt.Sprintf("CREATE TABLE %s (account TEXT NOT NULL PRIMARY KEY, plan TEXT NOT NULL)", table))
	must.NoError(t, err)

	_, err = q.ExecContext(t.Context(),
		fmt.Sprintf("INSERT INTO %s (account, plan) VALUES (?, ?)", table), testAccount, plan)
	must.NoError(t, err)

	return PlanSourceFunc(func(ctx context.Context, account string) (string, error) {
		var found string
		if scanErr := q.QueryRowContext(ctx,
			fmt.Sprintf("SELECT plan FROM %s WHERE account = ?", table), account).Scan(&found); scanErr != nil {
			return "", scanErr
		}

		return found, nil
	})
}

// TestQuotaSource_isNotOnTheCheckPath is the wiring entitlements and metering
// documentation both describe, counted.
//
// QuotaSource resolves the subject's plan, which reaches a database, and metering
// asks a QuotaSource what a subject's limit is. An enforcer that asked per Check
// therefore put that read on every request path a quota guards — which is the
// opposite of what Check is for, and what caching the resolved quota beside the
// total fixes. The assertion is made against the executor rather than against a
// store's querier so that it holds for both halves at once: metering's own total
// read and the quota source's plan lookup run on the same one, and a read either
// of them grew would show up here.
func TestQuotaSource_isNotOnTheCheckPath(T *testing.T) {
	T.Parallel()

	client, err := sqlite.NewDatabaseClient(T.Context(),
		&sqliteClientConfig{connectionString: filepath.Join(T.TempDir(), "entitlements.db")})
	must.NoError(T, err)
	T.Cleanup(func() { _ = client.Close() })

	prefix := fmt.Sprintf("ent_%d", meteringPrefix.Add(1))

	stmts, err := meteringmigrations.Statements(dialect.SQLite, prefix)
	must.NoError(T, err)
	must.SliceNotEmpty(T, stmts)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(T.Context(), stmt)
		must.NoError(T, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := metering.NewSQLStore(client, metering.WithTablePrefix(prefix))
	must.NoError(T, err)

	counter := &countingExecutor{q: client.Reader()}
	registry := newRegistry(T)

	quotas, err := NewQuotaSource(newCatalog(T), newDatabasePlans(T, counter, prefix+"_plans", planPro), registry)
	must.NoError(T, err)

	// The plan table's own creation and seeding ran on the counter too.
	counter.statements.Store(0)

	totals, err := memory.NewInMemoryCache[metering.CachedTotal](metering.DefaultStaleness)
	must.NoError(T, err)

	enforcer, err := metering.NewQuotaEnforcer(T.Context(), &metering.EnforcerConfig{},
		store, registry, counter, metering.WithEnforcerQuotaSource(quotas),
		metering.WithEnforcerCache(totals))
	must.NoError(T, err)

	first, err := enforcer.Check(T.Context(), testScope, testAccount, testMeter, 1)
	must.NoError(T, err)

	// The cold one pays for both: the plan lookup and the durable total.
	test.EqOp(T, int64(1000), first.Limit)
	test.False(T, first.Stale)
	test.EqOp(T, int64(2), counter.statements.Load())

	counter.statements.Store(0)

	second, err := enforcer.Check(T.Context(), testScope, testAccount, testMeter, 1)
	must.NoError(T, err)

	// None. Not "one instead of two" — the entry carries the total and the quota
	// it was resolved against, so a Check inside the staleness budget reaches
	// neither the source nor the store.
	test.EqOp(T, int64(0), counter.statements.Load())

	// And it is the catalog's limit that answered, rather than the zero a missing
	// quota would have made it — which would refuse every request for the length
	// of the budget.
	test.EqOp(T, first.Limit, second.Limit)
	test.True(T, second.Allowed)
	test.True(T, second.Stale)
}
