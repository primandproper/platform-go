package devices

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/signin/devices/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

// testUser is the person most of these tests record a login for.
const testUser = "user_01"

// testTTL is how long a recorded login lives unless a test wants another.
const testTTL = 30 * 24 * time.Hour

// testScope is a named tenant, deliberately not Global: a store that dropped its
// scope predicate would still pass every assertion made under Global, since the
// empty identifier is what an unscoped column holds anyway.
func testScope() tenancy.Scope { return tenancy.Of("tenant_a") }

// testClientConfig is the minimum database.ClientConfig a client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string { return c.connectionString }

// A container reports "ready" from its log line slightly before it accepts TCP
// connections, so these give IsReady room to ride that out; a SQLite client
// succeeds on the first ping and pays none of it.
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 30 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Second }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// fakeClock is a Clock whose time only moves when a test moves it, and whose
// ticker the test drives by hand.
type fakeClock struct {
	now    time.Time
	ticker chan time.Time
	mu     sync.Mutex
}

var _ clock.Clock = (*fakeClock)(nil)

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:    time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC),
		ticker: make(chan time.Time),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Since(t time.Time) time.Duration                  { return c.Now().Sub(t) }
func (c *fakeClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (c *fakeClock) NewTicker(time.Duration) clock.Ticker             { return &fakeTicker{c: c} }

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// tick releases one iteration of the background sweep loop.
func (c *fakeClock) tick() { c.ticker <- c.Now() }

// fakeTicker hands the loop the channel the test drives.
type fakeTicker struct {
	c *fakeClock
}

var _ clock.Ticker = (*fakeTicker)(nil)

func (t *fakeTicker) Chan() <-chan time.Time { return t.c.ticker }
func (t *fakeTicker) Stop()                  {}

// newTestClient builds a SQLite-backed client with the device table created.
//
// SQLite exercises the real SQL — the placeholder rendering, the upsert's
// conflict branch, the expanded set — without a container, so the store's core
// behavior is covered by `make test` rather than only by integration runs.
func newTestClient(tb testing.TB) database.Client {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "devices.db")})
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

// sighting is a login of userID's, seen from origin, living testTTL from now.
func (h *harness) sighting(familyID, userID string, origin Origin) *Sighting {
	return &Sighting{
		FamilyID:  familyID,
		UserID:    userID,
		Origin:    origin,
		ExpiresAt: h.clock.Now().Add(testTTL),
	}
}

// record writes a sighting in a transaction of its own, reporting what the
// store said.
func (h *harness) record(tb testing.TB, scope tenancy.Scope, sighting *Sighting) error {
	tb.Helper()

	return h.client.WithTransaction(tb.Context(), func(tx database.Tx) error {
		return h.store.Record(tb.Context(), tx, scope, sighting)
	})
}

// mustRecord is record, failing the test if it cannot.
func (h *harness) mustRecord(tb testing.TB, scope tenancy.Scope, sighting *Sighting) {
	tb.Helper()

	must.NoError(tb, h.record(tb, scope, sighting))
}

// listForUser reads one person's rows on the reader, failing the test if it
// cannot.
func (h *harness) listForUser(tb testing.TB, scope tenancy.Scope, userID string) []*Device {
	tb.Helper()

	recorded, err := h.store.ListForUser(tb.Context(), h.client.Reader(), scope, userID)
	must.NoError(tb, err)

	return recorded
}

// deleteForUser removes one person's rows in a transaction of its own.
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

// rowsIn counts the rows in one table. It is raw SQL in a test, which is the one
// place this package has any.
func rowsIn(tb testing.TB, client database.Client, table string) int {
	tb.Helper()

	var count int
	must.NoError(tb, client.Writer().
		QueryRowContext(tb.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count))

	return count
}

// recordingLogger counts what was logged as an error, for the one code path in
// this package whose only effect is a log line: the background sweep, which
// nothing is waiting on.
type recordingLogger struct {
	logging.Logger

	errors []string

	mu sync.Mutex
}

var _ logging.Logger = (*recordingLogger)(nil)

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{Logger: loggingnoop.NewLogger()}
}

func (l *recordingLogger) Error(whatWasHappening string, _ error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.errors = append(l.errors, whatWasHappening)
}

// The derivation methods hand back this same recorder, so that a logger named by
// observability.NewObserver still records.
func (l *recordingLogger) Clone() logging.Logger                    { return l }
func (l *recordingLogger) WithName(string) logging.Logger           { return l }
func (l *recordingLogger) WithValue(string, any) logging.Logger     { return l }
func (l *recordingLogger) WithValues(map[string]any) logging.Logger { return l }
func (l *recordingLogger) WithError(error) logging.Logger           { return l }
func (l *recordingLogger) WithSpan(trace.Span) logging.Logger       { return l }

// count reports how often one message was logged as an error.
func (l *recordingLogger) count(message string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	var n int
	for _, logged := range l.errors {
		if logged == message {
			n++
		}
	}

	return n
}
