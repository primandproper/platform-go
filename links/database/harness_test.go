package database

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/links"
	"github.com/primandproper/platform-go/v14/links/database/migrations"

	"github.com/primandproper/primitives-go/clock"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	"github.com/primandproper/primitives-go/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/observability/tracing/noop"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

const (
	testID      links.ID      = "8b1a9953c4611296a827abf8c47804d7"
	testAction  links.Action  = "magic_login"
	testSubject links.Subject = "user_123"
)

// mintedAt is the instant every record these tests write is created at.
//
// It carries no fractional seconds on purpose. SQLite has no date type, so a
// timestamp is text rendered to whole seconds there, and a fractional instant
// would come back truncated — which is the engine behaving as documented and
// would only make these assertions about the store read as assertions about
// that.
var mintedAt = time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)

// testClientConfig is the minimum database.ClientConfig a client needs.
type testClientConfig struct {
	connectionString string

	// maxOpenConns is one for SQLite, whose writers serialize on the file
	// anyway, and several for a container run — the case that proves one link
	// goes to one caller means nothing if the pool hands every contender the
	// same connection.
	maxOpenConns int
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string { return c.connectionString }

// A container reports "ready" from its log line slightly before it accepts TCP
// connections, so the first statement after construction can land on a socket
// that is still closing. These values give IsReady room to ride that out; a
// SQLite client succeeds on the first ping and pays none of it.
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
//
// Only the sweeper reads it: when a link expires and when its row may be
// collected are both decided by the Minter and arrive as arguments.
type fakeClock struct {
	now    time.Time
	ticker chan time.Time
	mu     sync.Mutex
}

var _ clock.Clock = (*fakeClock)(nil)

func newFakeClock() *fakeClock {
	return &fakeClock{now: mintedAt, ticker: make(chan time.Time)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Since(t time.Time) time.Duration                  { return c.Now().Sub(t) }
func (c *fakeClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func (c *fakeClock) NewTicker(_ time.Duration) clock.Ticker { return &fakeTicker{c: c} }

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

// newTestClient builds a SQLite-backed client with the action link table
// created.
//
// SQLite exercises the real SQL — the placeholder rendering, the transaction
// Resolve runs in, the guard that decides single use — without a container, so
// the store's core behavior is covered by `make test` rather than only by
// integration runs.
func newTestClient(tb testing.TB) database.Client {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "links.db")})
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

// newTestStore builds a store over a fresh SQLite database and a clock the test
// controls.
func newTestStore(tb testing.TB, opts ...Option) (*Store, *fakeClock) {
	tb.Helper()

	c := newFakeClock()

	store, err := New(&Config{}, newTestClient(tb), append([]Option{
		WithClock(c),
		WithLogger(loggingnoop.NewLogger()),
		WithTracerProvider(tracingnoop.NewTracerProvider()),
	}, opts...)...)
	must.NoError(tb, err)

	return store, c
}

// activeRecord is a link that is live for an hour and collectable an hour after
// that.
func activeRecord() *links.Record {
	return &links.Record{
		CreatedAt:  mintedAt,
		ExpiresAt:  mintedAt.Add(time.Hour),
		PurgeAfter: mintedAt.Add(2 * time.Hour),
		Action:     testAction,
		Subject:    testSubject,
		Version:    links.RecordVersion,
		State:      links.StateActive,
	}
}

// put writes one record, failing the test if it cannot.
func put(tb testing.TB, store *Store, id links.ID, record *links.Record) {
	tb.Helper()

	must.NoError(tb, store.Put(tb.Context(), id, record))
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

// The derivation methods hand back this same recorder, so that a logger named
// by observability.NewObserver still records.
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

// rowsIn counts the rows in one table, for the assertions about which table a
// namespaced store addressed. It is raw SQL in a test, which is the one place
// this package has any: the point of the assertion is the table name.
func rowsIn(tb testing.TB, client database.Client, table string) int {
	tb.Helper()

	var count int
	must.NoError(tb, client.Writer().
		QueryRowContext(tb.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count))

	return count
}
