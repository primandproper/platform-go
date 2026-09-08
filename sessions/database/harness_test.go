package database

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/sessions"
	"github.com/primandproper/platform-go/v14/sessions/database/migrations"

	"github.com/primandproper/primitives-go/clock"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/observability/tracing/noop"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// principal is the payload the tests store.
type principal struct {
	UserID string
	Admin  bool
}

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string { return c.connectionString }

// A container reports "ready" from its log line slightly before it accepts TCP
// connections, so the first statement after construction can land on a socket
// that is still closing. These values give IsReady room to ride that out; a
// SQLite client succeeds on the first ping and pays none of it.
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 30 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Second }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// fakeClock is a Clock whose time only moves when a test moves it.
type fakeClock struct {
	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*fakeClock)(nil)

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)}
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

// newTestClient builds a SQLite-backed client with the session table created.
//
// SQLite exercises the real SQL — the placeholder rendering, the insert-ignore
// clause, the transaction Rename runs in — without a container, so the
// backend's core behavior is covered by `make test` rather than only by
// integration runs.
func newTestClient(t *testing.T) database.Client {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "sessions.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	createTable(t, client, dialect.SQLite, DefaultTablePrefix)

	return client
}

// createTable runs the shipped DDL against a client.
func createTable(t *testing.T, client database.Client, d dialect.Dialect, prefix string) {
	t.Helper()

	stmts, err := migrations.Statements(d, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}
}

// newTestBackend builds a backend over a fresh SQLite database and a clock the
// test controls.
func newTestBackend(t *testing.T, opts ...Option) (*Backend[principal], *fakeClock) {
	t.Helper()

	c := newFakeClock()

	backend, err := NewBackend[principal](&Config{}, newTestClient(t), append([]Option{
		WithClock(c),
		WithLogger(loggingnoop.NewLogger()),
		WithTracerProvider(tracingnoop.NewTracerProvider()),
	}, opts...)...)
	must.NoError(t, err)

	return backend, c
}

// testRecord is one live record, stamped at the clock's current instant and
// held by nobody — the global scope and the empty principal, which is what
// Store.New writes.
func testRecord(c *fakeClock, userID string) *sessions.Record[principal] {
	return testHeldRecord(c, sessions.Holder{Scope: tenancy.Global()}, sessions.Metadata{}, userID)
}

// overlongMetadata is a client's self-description at a width no VARCHAR in this
// module's DDL would hold — 300 characters, past the 255 the indexed columns
// declare. A device name is a user agent as often as not, and user agents run
// past 255 characters in the wild.
func overlongMetadata() sessions.Metadata {
	return sessions.Metadata{
		DeviceName:  strings.Repeat("d", 300),
		IPAddress:   strings.Repeat("i", 300),
		UserAgent:   strings.Repeat("u", 300),
		LoginMethod: strings.Repeat("m", 300),
	}
}

// assertOverlongMetadataRoundTrips writes a record whose four device columns are
// all past any VARCHAR width and reads it back through both paths that return
// metadata.
//
// It is the one assertion every dialect makes, because the failure it guards is
// a disagreement between them: MySQL's create statement is INSERT IGNORE, whose
// IGNORE downgrades a truncation error to a warning, so a narrow column there
// would cut the value and say nothing while Postgres and SQLite stored it whole.
// The holder is the caller's so that a suite sharing one table can keep this
// record away from its neighbors.
func assertOverlongMetadataRoundTrips(
	t *testing.T,
	backend *Backend[principal],
	c *fakeClock,
	holder sessions.Holder,
	id string,
) {
	t.Helper()

	ctx := t.Context()
	want := overlongMetadata()

	must.NoError(t, backend.Create(ctx, id, testHeldRecord(c, holder, want, "u_long"), time.Hour))

	loaded, err := backend.Load(ctx, id)
	must.NoError(t, err)
	test.EqOp(t, want, loaded.Metadata)

	held, err := backend.ListHeld(ctx, holder)
	must.NoError(t, err)
	must.SliceLen(t, 1, held)
	test.EqOp(t, want, held[0].Record.Metadata)
}

// testHeldRecord is the same record attributed to somebody.
func testHeldRecord(
	c *fakeClock,
	holder sessions.Holder,
	metadata sessions.Metadata,
	userID string,
) *sessions.Record[principal] {
	now := c.Now().UTC().Truncate(time.Microsecond)

	return &sessions.Record[principal]{
		CreatedAt:  now,
		LastSeenAt: now,
		Data:       &principal{UserID: userID},
		Holder:     holder,
		Metadata:   metadata,
		Version:    1,
	}
}
