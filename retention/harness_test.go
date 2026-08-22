package retention

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	auditmigrations "github.com/primandproper/platform-go/v13/audit/migrations"
	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"

	"github.com/shoenig/test/must"
)

// baseTime is the instant this suite works relative to.
var baseTime = time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)

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
// time and these tests need weeks of it, so they control the clock rather than
// race the wall.
//
// A synctest bubble would normally spare us a double — that is the contract
// clock.Clock advertises — but it advances fake time only once every goroutine
// in the bubble is durably blocked, and these tests drive a real SQLite file.
// Hence a stub, built on the generated mock so the methods nothing calls fail
// loudly instead of lying.
type stubClock struct {
	*clockmock.ClockMock

	now time.Time

	// slept records every pause the sweeper took, so a test can assert the
	// pacing happened without waiting for it.
	slept []time.Duration

	// pauseExpires makes every pause report a deadline, standing in for a job
	// timeout landing partway through a policy that has not drained.
	pauseExpires bool

	mu sync.Mutex
}

var _ clock.Clock = (*stubClock)(nil)

func newStubClock() *stubClock {
	c := &stubClock{now: baseTime}

	c.ClockMock = &clockmock.ClockMock{
		NowFunc:   c.read,
		SinceFunc: func(t time.Time) time.Duration { return c.read().Sub(t) },
		SleepFunc: c.sleep,

		// NewTickerFunc is deliberately left nil. Nothing here tickers — the
		// Sweeper is driven by a jobs.Scheduler — and if that changes, moq
		// panics rather than quietly handing back a ticker nobody meant.
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

// sleep records the pause and returns immediately, honoring a cancelled context
// the way the real clock does.
func (c *stubClock) sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.slept = append(c.slept, d)
	expired := c.pauseExpires
	c.mu.Unlock()

	if expired {
		return context.DeadlineExceeded
	}

	return ctx.Err()
}

func (c *stubClock) pauses() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]time.Duration(nil), c.slept...)
}

// widgetsTable is the table these tests sweep. It stands in for whatever an
// application has with a lifetime, and is deliberately not owned by this
// package: retention deletes from other people's tables, which is the whole
// premise.
const widgetsTable = "widgets"

// newTestClient builds a SQLite-backed database.Client holding the widgets
// table and the audit tables. SQLite exercises the real SQL — placeholder
// rendering, the bounded subselect, the ordering — without a container.
func newTestClient(t *testing.T) database.Client {
	t.Helper()

	ctx := t.Context()

	client, err := sqlite.NewDatabaseClient(ctx, &testClientConfig{
		connectionString: filepath.Join(t.TempDir(), "retention.db"),
	})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.Writer().ExecContext(ctx,
		"CREATE TABLE widgets (id TEXT PRIMARY KEY, created_at DATETIME NOT NULL, expires_at DATETIME)")
	must.NoError(t, err)

	stmts, err := auditmigrations.Statements(dialect.SQLite, "")
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(ctx, stmt)
		must.NoError(t, execErr, must.Sprintf("applying %q", stmt))
	}

	return client
}

// insertWidgets writes n rows whose created_at is at, and whose expires_at is
// the same instant, with IDs prefixed so a test can tell cohorts apart.
func insertWidgets(t *testing.T, client database.Client, prefix string, at time.Time, n int) {
	t.Helper()

	for i := range n {
		_, err := client.Writer().ExecContext(t.Context(),
			"INSERT INTO widgets (id, created_at, expires_at) VALUES (?, ?, ?)",
			prefix+"-"+strconv.Itoa(i), at.UTC(), at.UTC(),
		)
		must.NoError(t, err)
	}
}

// countWidgets reports how many rows survive.
func countWidgets(t *testing.T, client database.Client) int64 {
	t.Helper()

	var n int64
	must.NoError(t, client.Reader().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM widgets").Scan(&n))

	return n
}

// widgetIDs reports the surviving rows in insertion order, so a test can assert
// which cohort a batch took.
func widgetIDs(t *testing.T, client database.Client) []string {
	t.Helper()

	rows, err := client.Reader().QueryContext(t.Context(), "SELECT id FROM widgets ORDER BY created_at, id")
	must.NoError(t, err)

	defer func() { must.NoError(t, rows.Close()) }()

	var ids []string
	for rows.Next() {
		var id string
		must.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	must.NoError(t, rows.Err())

	return ids
}

// newTestSweeper builds a Sweeper over policies with the stub clock attached,
// which is what every test here wants.
func newTestSweeper(t *testing.T, client database.Client, policies []Policy, opts ...SweeperOption) (*Sweeper, *stubClock) {
	t.Helper()

	c := newStubClock()

	sweeper, err := NewSweeper(
		t.Context(),
		&SweeperConfig{},
		client,
		policies,
		append([]SweeperOption{WithSweeperClock(c)}, opts...)...,
	)
	must.NoError(t, err)

	return sweeper, c
}
