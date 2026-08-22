package outbox

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/outbox/migrations"

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

		// Reached only by Run, whose test sets both intervals to an hour so
		// that the drain on Close does the publishing. A real ticker is
		// therefore constructed and stopped without ever firing.
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

	must.NoError(t, client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
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
