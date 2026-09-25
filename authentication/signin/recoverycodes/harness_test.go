package recoverycodes

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"
	"github.com/primandproper/primitives-go/v2/random"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// testUser is the person most of these tests mint a set for.
const testUser = "user_01"

// testCount is how many codes a replacement asks for unless a test wants
// another. It is signin's own default, so a test that only passes with fewer
// fails here rather than in a deployment.
const testCount = 8

// testScope is a named tenant, deliberately not Global: a store that dropped its
// scope predicate would still pass every assertion made under Global, since the
// empty identifier is what an unscoped column holds anyway.
func testScope() tenancy.Scope { return tenancy.Of("tenant_a") }

// testClientConfig is the minimum database.ClientConfig a client needs.
type testClientConfig struct {
	connectionString string

	// maxOpenConns is one for SQLite, whose writers serialize on the file
	// anyway, and several for a container run — the case that proves one code
	// goes to one caller means nothing if the pool hands every contender the
	// same connection.
	maxOpenConns int
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string { return c.connectionString }

// A container reports "ready" from its log line slightly before it accepts TCP
// connections, so these give IsReady room to ride that out; a SQLite client
// succeeds on the first ping and pays none of it.
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

// fakeClock is a Clock whose time only moves when a test moves it.
type fakeClock struct {
	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*fakeClock)(nil)

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Since(t time.Time) time.Duration                  { return c.Now().Sub(t) }
func (c *fakeClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (c *fakeClock) NewTicker(time.Duration) clock.Ticker             { panic("nothing here ticks") }

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// newTestClient builds a SQLite-backed client with the code table created.
//
// SQLite exercises the real SQL — the placeholder rendering, the guarded write,
// the composite primary key — without a container, so the store's core behavior
// is covered by `make test` rather than only by integration runs.
func newTestClient(tb testing.TB) database.Client {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "recoverycodes.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	createTable(tb, client, dialect.SQLite, DefaultTablePrefix)

	return client
}

// createTable runs the shipped DDL against a client.
func createTable(tb testing.TB, client database.Client, d dialect.Dialect, prefix string) {
	tb.Helper()

	stmts, err := migrations.Statements(d, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr)
	}
}

// harness is a store, the client its callers' transactions are opened on, and a
// clock the test controls.
type harness struct {
	store  *SQLStore
	client database.Client
	clock  *fakeClock
}

// newHarness builds a store over a fresh SQLite database.
func newHarness(tb testing.TB, opts ...Option) *harness {
	tb.Helper()

	return newHarnessOn(tb, newTestClient(tb), &Config{}, opts...)
}

// newHarnessOn builds a store over a client somebody else created the table on.
func newHarnessOn(tb testing.TB, client database.Client, cfg *Config, opts ...Option) *harness {
	tb.Helper()

	c := newFakeClock()

	store, err := NewSQLStore(cfg, client, append([]Option{
		WithClock(c),
		WithLogger(loggingnoop.NewLogger()),
		WithTracerProvider(tracingnoop.NewTracerProvider()),
	}, opts...)...)
	must.NoError(tb, err)

	return &harness{store: store, client: client, clock: c}
}

// replace mints a set in a transaction of its own, reporting what the store said.
func (h *harness) replace(tb testing.TB, scope tenancy.Scope, userID string, count int) ([]string, error) {
	tb.Helper()

	var codes []string

	err := h.client.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var replaceErr error
		codes, replaceErr = h.store.Replace(tb.Context(), tx, scope, userID, count)

		return replaceErr
	})

	return codes, err
}

// mint is replace for the usual user, failing the test if it cannot.
func (h *harness) mint(tb testing.TB, userID string) []string {
	tb.Helper()

	codes, err := h.replace(tb, testScope(), userID, testCount)
	must.NoError(tb, err)
	must.SliceLen(tb, testCount, codes)

	return codes
}

// consume spends a code in a transaction of its own, which is what a caller with
// nothing else to write does.
func (h *harness) consume(tb testing.TB, scope tenancy.Scope, userID, code string) error {
	tb.Helper()

	return h.client.WithTransaction(tb.Context(), func(tx database.Tx) error {
		return h.store.Consume(tb.Context(), tx, scope, userID, code)
	})
}

// deleteForUser removes a user's set in a transaction of its own.
func (h *harness) deleteForUser(tb testing.TB, scope tenancy.Scope, userID string) (int64, error) {
	tb.Helper()

	var deleted int64

	err := h.client.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var deleteErr error
		deleted, deleteErr = h.store.DeleteForUser(tb.Context(), tx, scope, userID)

		return deleteErr
	})

	return deleted, err
}

// remaining counts a user's unspent codes on the reader.
func (h *harness) remaining(tb testing.TB, scope tenancy.Scope, userID string) int {
	tb.Helper()

	n, err := h.store.Remaining(tb.Context(), h.client.Reader(), scope, userID)
	must.NoError(tb, err)

	return n
}

// rowsIn counts the rows in one table. It is raw SQL in a test, which is the one
// place this package has any.
func rowsIn(tb testing.TB, client database.Client, table string) int {
	tb.Helper()

	var count int
	must.NoError(tb, client.Writer().
		QueryRowContext(tb.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count))

	return count
}

// constantGenerator hands back the same bytes every time, for the case only the
// primary key decides: a code repeated inside one set is a failed replacement
// rather than a person shown a code that was never stored.
type constantGenerator struct {
	raw []byte
}

var _ random.Generator = (*constantGenerator)(nil)

func (g *constantGenerator) GenerateHexEncodedString(context.Context, int) (string, error) {
	return string(g.raw), nil
}

func (g *constantGenerator) GenerateBase32EncodedString(context.Context, int) (string, error) {
	return string(g.raw), nil
}

func (g *constantGenerator) GenerateBase64EncodedString(context.Context, int) (string, error) {
	return string(g.raw), nil
}

func (g *constantGenerator) GenerateRawBytes(context.Context, int) ([]byte, error) {
	return g.raw, nil
}
