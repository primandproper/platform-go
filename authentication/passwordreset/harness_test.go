package passwordreset

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations"

	"github.com/primandproper/primitives-go/clock"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	"github.com/primandproper/primitives-go/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/observability/tracing/noop"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

// testUserID is the principal most of these tests reset.
const testUserID = "user_01"

// testScope is a named tenant, deliberately not Global: a store that dropped its
// scope predicate would still pass every assertion made under Global, since the
// empty identifier is what an unscoped column holds anyway.
func testScope() tenancy.Scope { return tenancy.Of("tenant_a") }

// testClientConfig is the minimum database.ClientConfig a client needs.
type testClientConfig struct {
	connectionString string

	// maxOpenConns is one for SQLite, whose writers serialize on the file
	// anyway, and several for a container run — the case that proves one token
	// goes to one consumer means nothing if the pool hands every contender the
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
type fakeClock struct {
	now    time.Time
	ticker chan time.Time
	mu     sync.Mutex
}

var _ clock.Clock = (*fakeClock)(nil)

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:    time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC),
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

// newTestClient builds a SQLite-backed client with the token table created.
//
// SQLite exercises the real SQL — the placeholder rendering, the transaction
// Consume runs in, the unique index the digest column carries — without a
// container, so the store's core behavior is covered by `make test` rather than
// only by integration runs.
func newTestClient(tb testing.TB) database.Client {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "passwordreset.db")})
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
func newTestStore(tb testing.TB, opts ...Option) (*SQLStore, *fakeClock) {
	tb.Helper()

	c := newFakeClock()

	store, err := NewSQLStore(&Config{}, newTestClient(tb), append([]Option{
		WithClock(c),
		WithLogger(loggingnoop.NewLogger()),
		WithTracerProvider(tracingnoop.NewTracerProvider()),
	}, opts...)...)
	must.NoError(tb, err)

	return store, c
}

// withTx runs fn inside a real transaction on the store's client.
//
// Every write in these tests goes through it, because the store opens no
// transaction of its own any more — and it is also the production shape for a
// caller with nothing to join: Client.WithTransaction, with the Tx passed
// straight through. A refusal rolls the transaction back and comes back
// unwrapped, which is what lets these tests keep asserting on the sentinel.
func withTx(tb testing.TB, store *SQLStore, fn func(tx database.Tx) error) error {
	tb.Helper()

	return store.db.WithTransaction(tb.Context(), fn)
}

// issue mints one token for the usual principal, failing the test if it cannot.
func issue(tb testing.TB, store *SQLStore, ttl time.Duration) *Issuance {
	tb.Helper()

	issuance, err := issueFor(tb, store, testScope(), testUserID, ttl)
	must.NoError(tb, err)
	must.NotNil(tb, issuance)

	return issuance
}

// issueFor mints one token for a named principal in a named scope, reporting
// what the store said rather than failing on it.
func issueFor(
	tb testing.TB,
	store *SQLStore,
	scope tenancy.Scope,
	userID string,
	ttl time.Duration,
) (*Issuance, error) {
	tb.Helper()

	var issuance *Issuance

	err := withTx(tb, store, func(tx database.Tx) error {
		var issueErr error
		issuance, issueErr = store.Issue(tb.Context(), tx, scope, userID, ttl)

		return issueErr
	})

	return issuance, err
}

// verify resolves a secret through the write pool, which is the executor a page
// load holds: a replica can answer "not found" for a link that arrived seconds
// ago. The cases that want the transaction's own view pass a Tx instead.
func verify(tb testing.TB, store *SQLStore, scope tenancy.Scope, secret string) (*Token, error) {
	tb.Helper()

	return store.Verify(tb.Context(), store.db.Writer(), scope, secret)
}

// consume spends a secret in a transaction of its own, which is what a caller
// with nothing else to write does.
func consume(tb testing.TB, store *SQLStore, scope tenancy.Scope, secret string) (*Token, error) {
	tb.Helper()

	var token *Token

	err := withTx(tb, store, func(tx database.Tx) error {
		var consumeErr error
		token, consumeErr = store.Consume(tb.Context(), tx, scope, secret)

		return consumeErr
	})

	return token, err
}

// revokeForUser destroys one principal's outstanding tokens in a transaction of
// its own.
func revokeForUser(tb testing.TB, store *SQLStore, scope tenancy.Scope, userID string) (int64, error) {
	tb.Helper()

	var revoked int64

	err := withTx(tb, store, func(tx database.Tx) error {
		var revokeErr error
		revoked, revokeErr = store.RevokeForUser(tb.Context(), tx, scope, userID)

		return revokeErr
	})

	return revoked, err
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

// count reports how often one message was logged as an error. It counts by
// message rather than in total because Sweep records its own failure through the
// same logger, and the loop's line is the one under test.
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
// this package still has any: the point of the assertion is the table name.
func rowsIn(t *testing.T, client database.Client, table string) int {
	t.Helper()

	var count int
	must.NoError(t, client.Writer().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count))

	return count
}
