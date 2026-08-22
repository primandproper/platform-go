package database

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	"github.com/primandproper/platform-go/v13/authentication/webauthn/database/migrations"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

// testClientConfig is the minimum database.ClientConfig a client needs.
type testClientConfig struct {
	connectionString string

	// maxOpenConns is one for SQLite, whose writers serialize on the file
	// anyway, and several for a container run — the case that proves one
	// ceremony goes to one consumer means nothing if the pool hands every
	// contender the same connection.
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
	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*fakeClock)(nil)

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Since(t time.Time) time.Duration                  { return c.Now().Sub(t) }
func (c *fakeClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (c *fakeClock) NewTicker(_ time.Duration) clock.Ticker           { panic("not used") }

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// newTestClient builds a SQLite-backed client with the ceremony session table
// created.
//
// SQLite exercises the real SQL — the placeholder rendering, the dialect's
// upsert clause, the transaction Consume runs in — without a container, so the
// store's core behavior is covered by `make test` rather than only by
// integration runs.
func newTestClient(tb testing.TB) database.Client {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "webauthn.db")})
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
func newTestStore(tb testing.TB, opts ...Option) (*SessionStore, *fakeClock) {
	tb.Helper()

	c := newFakeClock()

	store, err := NewSessionStore(&Config{}, newTestClient(tb), append([]Option{
		WithClock(c),
		WithLogger(loggingnoop.NewLogger()),
		WithTracerProvider(tracingnoop.NewTracerProvider()),
	}, opts...)...)
	must.NoError(tb, err)

	return store, c
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
// message rather than in total because Sweep records its own failure through
// the same logger, and the loop's line is the one under test.
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

// testSession is one ceremony's worth of state, in every field a Finish reads.
func testSession(challenge string) *webauthn.SessionData {
	return &webauthn.SessionData{
		Challenge:            challenge,
		RelyingPartyID:       "example.com",
		UserID:               []byte("user-handle"),
		AllowedCredentialIDs: [][]byte{[]byte("credential-one")},
		UserVerification:     protocol.VerificationPreferred,
		Expires:              time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond),
	}
}
