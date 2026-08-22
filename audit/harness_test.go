package audit

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/audit/migrations"
	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"

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

// stubClock is a manually advanced clock. Retention is a function of elapsed
// time and the tests need years of it, so they control the clock rather than
// race the wall.
//
// A synctest bubble would normally spare us a double — that is the contract
// clock.Clock advertises — but it advances fake time only once every goroutine
// in the bubble is durably blocked, and these tests drive a real SQLite file.
// Neither the cgo-free driver's syscalls nor database/sql's background
// goroutines block durably, so time would not jump on command. Hence a stub,
// built on the generated mock so the methods nothing calls fail loudly instead
// of lying.
type stubClock struct {
	*clockmock.ClockMock

	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*stubClock)(nil)

func newStubClock() *stubClock {
	c := &stubClock{now: time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)}

	c.ClockMock = &clockmock.ClockMock{
		NowFunc:   c.read,
		SinceFunc: func(t time.Time) time.Duration { return c.read().Sub(t) },

		// Reached only by Run, whose test sets the interval long enough that the
		// ticker never fires and the sweeping is driven by an explicit Sweep. A
		// real ticker is therefore constructed and stopped without firing.
		NewTickerFunc: clock.NewClock().NewTicker,

		// SleepFunc is deliberately left nil. Nothing here sleeps, and if that
		// changes, moq panics — which is what we want, because the obvious stub
		// (return nil) would silently ignore the context and let a cancellation
		// bug pass.
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

// newTestClient builds a SQLite-backed database.Client with the audit tables
// already created. SQLite exercises the real SQL — placeholder rendering, the
// chain uniqueness constraint, the prune bounds arithmetic — without a
// container.
func newTestClient(t *testing.T) database.Client {
	t.Helper()

	ctx := t.Context()

	client, err := sqlite.NewDatabaseClient(ctx, &testClientConfig{connectionString: filepath.Join(t.TempDir(), "audit.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	applyMigrations(t, client, dialect.SQLite, DefaultTablePrefix)

	return client
}

// applyMigrations creates the audit tables for a prefix.
func applyMigrations(t *testing.T, client database.Client, d dialect.Dialect, prefix string) {
	t.Helper()

	stmts, err := migrations.Statements(d, prefix)
	must.NoError(t, err)

	if len(stmts) == 0 {
		t.Fatal("no audit DDL statements rendered")
	}

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}
}

// newTestRecorder builds a Recorder over the stub clock.
func newTestRecorder(t *testing.T, c *stubClock, opts ...RecorderOption) Recorder {
	t.Helper()

	r, err := NewRecorder(dialect.SQLite, append([]RecorderOption{WithRecorderClock(c)}, opts...)...)
	must.NoError(t, err)

	return r
}

// newTestReader builds a Reader over the supplied client.
func newTestReader(t *testing.T, client database.Client, opts ...ReaderOption) Reader {
	t.Helper()

	r, err := NewReader(client, opts...)
	must.NoError(t, err)

	return r
}

// record writes entries through a Recorder inside a transaction, the way a
// caller would.
func record(t *testing.T, client database.Client, r Recorder, entries ...*Entry) {
	t.Helper()

	must.NoError(t, client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
		return r.Record(t.Context(), q, entries...)
	}))
}

// entryFor builds a minimally valid entry for a scope.
func entryFor(scope, resourceID string) *Entry {
	return &Entry{
		EventType:    EventUpdated,
		ResourceType: "recipe",
		ResourceID:   resourceID,
		Scope:        scope,
		Actor:        Actor{ID: "user_1", Type: ActorUser, IP: "203.0.113.7"},
		Changes:      map[string]Change{"name": {Old: "old", New: "new"}},
	}
}

// countRows returns the number of rows matching the supplied WHERE clause.
func countRows(t *testing.T, client database.Client, table, where string) int {
	t.Helper()

	var n int
	must.NoError(t, client.Reader().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table+" WHERE "+where).
		Scan(&n))

	return n
}

// exec runs a statement against the write handle, for the tests that tamper
// with the table on purpose.
func exec(t *testing.T, client database.Client, query string, args ...any) {
	t.Helper()

	_, err := client.Writer().ExecContext(t.Context(), query, args...)
	must.NoError(t, err)
}

// ctxFor is a context for helpers that cannot take *testing.T.
func ctxFor(t *testing.T) context.Context {
	t.Helper()

	return t.Context()
}
