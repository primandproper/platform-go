package outbox

import (
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/outbox/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	clockmock "github.com/primandproper/primitives-go/v2/clock/mock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"

	"github.com/shoenig/test/must"
)

// testClientConfig is the minimum database.ClientConfig a test client needs.
type testClientConfig struct {
	connectionString string

	// maxOpenConns is one unless a suite asks for more. One is right for the
	// SQLite harness, which has a single writer anyway, and it is what the
	// container suites ran on until a test needed two transactions open at
	// once: a pool of one serializes them into a queue, which is the shape
	// that cannot observe two relays contending at all.
	maxOpenConns int
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64       { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int             { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int {
	if c.maxOpenConns > 0 {
		return c.maxOpenConns
	}

	return 1
}
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// stubClock is a manually advanced clock. The relay reads time at claim,
// publish, and failure, so tests that assert on backoff need to control it
// rather than race the wall clock.
//
// A synctest bubble would normally spare us a double entirely — that is the
// contract clock.Clock advertises — but it advances fake time only once every
// goroutine in the bubble is durably blocked, and these tests drive the relay
// synchronously against a real SQLite file. Neither the cgo-free driver's
// syscalls nor database/sql's background goroutines block durably, so time
// would not jump on command. Hence a stub, built on the generated mock so the
// methods nothing calls fail loudly instead of lying.
type stubClock struct {
	*clockmock.ClockMock

	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*stubClock)(nil)

func newStubClock() *stubClock {
	c := &stubClock{now: time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)}

	c.ClockMock = &clockmock.ClockMock{
		NowFunc:   c.read,
		SinceFunc: func(t time.Time) time.Duration { return c.read().Sub(t) },

		// Reached only by Run, whose lifecycle test sets both intervals to an
		// hour so that no tick can fire. A real ticker is therefore constructed
		// and stopped without ever firing.
		NewTickerFunc: clock.NewClock().NewTicker,

		// SleepFunc is deliberately left nil. Nothing in the relay sleeps, and
		// if that changes, moq panics — which is what we want, because the
		// obvious stub (return nil) would silently ignore the context and let
		// a cancellation bug pass.
	}

	return c
}

func (c *stubClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *stubClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// newTestClient builds a SQLite-backed database.Client with the outbox table
// already created. SQLite exercises the real SQL — placeholder rendering, the
// ordering predicate, the lease arithmetic — without a container.
func newTestClient(t *testing.T) database.Client {
	t.Helper()

	ctx := t.Context()

	client, err := sqlite.NewDatabaseClient(ctx, &testClientConfig{connectionString: filepath.Join(t.TempDir(), "outbox.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, DefaultTablePrefix)
	must.NoError(t, err)

	if len(stmts) == 0 {
		t.Fatal("no outbox DDL statements rendered")
	}

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(ctx, stmt)
		must.NoError(t, execErr)
	}

	return client
}

// enqueue writes messages through a Writer inside a transaction, the way a
// caller would.
func enqueue(t *testing.T, client database.Client, w *Writer, msgs ...Message) {
	t.Helper()

	must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
		return w.Enqueue(t.Context(), q, msgs...)
	}))
}

// countRows returns the number of rows matching the supplied WHERE clause.
func countRows(t *testing.T, client database.Client, where string) int {
	t.Helper()

	var n int
	must.NoError(t, client.Reader().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM outbox_messages WHERE "+where).
		Scan(&n))

	return n
}

// warnedLine is one Warn a component wrote, and the values its logger carried
// when it did.
type warnedLine struct {
	values  map[string]any
	message string
}

// recordingLogger keeps the Warn lines a component writes, so a test can assert
// on the one the quarantine reap emits — which is the last record that a
// discarded event ever existed, and therefore the one thing about that pass
// worth pinning.
//
// It embeds the noop logger and overrides only what it needs: everything else
// this package logs is already observed by the assertions around it. Derived
// loggers share the root's slice, because a component builds the line's values
// through WithValues and logs through what that returned.
type recordingLogger struct {
	logging.Logger

	lines  *[]warnedLine
	values map[string]any
	mu     *sync.Mutex
}

var _ logging.Logger = (*recordingLogger)(nil)

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{
		Logger: loggingnoop.NewLogger(),
		lines:  &[]warnedLine{},
		values: map[string]any{},
		mu:     &sync.Mutex{},
	}
}

func (l *recordingLogger) Warn(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	*l.lines = append(*l.lines, warnedLine{message: message, values: maps.Clone(l.values)})
}

func (l *recordingLogger) warnings() []warnedLine {
	l.mu.Lock()
	defer l.mu.Unlock()

	return slices.Clone(*l.lines)
}

func (l *recordingLogger) with(values map[string]any) logging.Logger {
	merged := make(map[string]any, len(l.values)+len(values))
	maps.Copy(merged, l.values)
	maps.Copy(merged, values)

	return &recordingLogger{Logger: l.Logger, lines: l.lines, values: merged, mu: l.mu}
}

func (l *recordingLogger) WithValue(key string, value any) logging.Logger {
	return l.with(map[string]any{key: value})
}

func (l *recordingLogger) WithValues(values map[string]any) logging.Logger { return l.with(values) }
func (l *recordingLogger) Clone() logging.Logger                           { return l.with(nil) }
func (l *recordingLogger) WithName(string) logging.Logger                  { return l.with(nil) }
