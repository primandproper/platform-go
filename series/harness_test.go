package series

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/series/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// testClientConfig is the minimum database.ClientConfig a client needs.
//
// maxOpenConns is 1 when unset, which is what SQLite gets. A real server's
// client asks for more, so a transaction the suite holds open does not starve
// the pool.
type testClientConfig struct {
	connectionString string
	maxOpenConns     int
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return max(1, c.maxOpenConns) }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// The tenants the suite stores series in. otherScope is the neighbor whose rows
// must never appear in testScope's answers.
var (
	testScope  = tenancy.Of("studio_1")
	otherScope = tenancy.Of("studio_2")
)

// prefixCounter names fresh tables per subtest, so subtests sharing one
// database never share rows.
var prefixCounter atomic.Uint64

// storeEnv is one live database plus the dialect to emit SQL for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newStore migrates uniquely prefixed tables and returns a Store over them.
func (e *storeEnv) newStore(t *testing.T) *SQLStore {
	t.Helper()

	store, err := NewSQLStore(e.client, WithTablePrefix(e.migrate(t)))
	must.NoError(t, err)

	// The fixtures name fixed dates, so the clock the write bounds measure
	// from is fixed beside them rather than read off a database whose now
	// walks away from them.
	store.now = func() time.Time { return storeNow }

	return store
}

// storeNow is the instant every store here takes as now: a few months into
// weeklyUTC, so its start is inside MaxWriteBehind and everything covers no
// more than MaxWriteAhead.
var storeNow = time.Date(2025, time.December, 1, 0, 0, 0, 0, time.UTC)

// migrate renders uniquely prefixed tables and returns the prefix.
func (e *storeEnv) migrate(t *testing.T) string {
	t.Helper()

	prefix := fmt.Sprintf("sr_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return prefix
}

// newSQLiteEnv builds a SQLite-backed environment, which exercises the real SQL
// without a container.
func newSQLiteEnv(t *testing.T) *storeEnv {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "series.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// inTx runs fn inside a transaction and reports what fn returned, rather than
// asserting on it: a refused write is what half of these cases are about.
func (e *storeEnv) inTx(tb testing.TB, fn func(tx database.Tx) error) error {
	tb.Helper()

	return e.client.WithTransaction(tb.Context(), fn)
}

// reader is the executor an ordinary read runs on.
func (e *storeEnv) reader() database.SQLQueryExecutor { return e.client.Reader() }

// mustCreate stores a rule in a transaction of its own.
func (e *storeEnv) mustCreate(tb testing.TB, store *SQLStore, scope tenancy.Scope, rule *Rule) *Series {
	tb.Helper()

	var created *Series

	must.NoError(tb, e.inTx(tb, func(tx database.Tx) error {
		var err error
		created, err = store.CreateSeries(tb.Context(), tx, scope, rule)

		return err
	}))

	return created
}

// mustMaterialize writes a series out to through in a transaction of its own.
func (e *storeEnv) mustMaterialize(tb testing.TB, store *SQLStore, scope tenancy.Scope, id string, through time.Time) int64 {
	tb.Helper()

	var written int64

	must.NoError(tb, e.inTx(tb, func(tx database.Tx) error {
		var err error
		written, err = store.Materialize(tb.Context(), tx, scope, id, through)

		return err
	}))

	return written
}

// occurrences reads one series' occurrences over a window wide enough for every
// case here.
func (e *storeEnv) occurrences(tb testing.TB, store *SQLStore, scope tenancy.Scope, seriesID string) []*Occurrence {
	tb.Helper()

	got, err := store.ListSeriesOccurrences(tb.Context(), e.reader(), scope, seriesID, everything)
	must.NoError(tb, err)

	return got
}

// weeklyUTC is a weekly rule at 16:00 UTC on Tuesdays from September 2 2025.
// UTC keeps the arithmetic in the assertions legible; the zone handling is
// rule_test.go's.
func weeklyUTC() *Rule {
	return &Rule{
		StartsOn:      Date{Year: 2025, Month: time.September, Day: 2},
		TimeZone:      "UTC",
		Weekday:       time.Tuesday,
		StartMinute:   16 * 60,
		IntervalWeeks: 1,
	}
}

// tuesday is the nth Tuesday of weeklyUTC, counting its first as zero.
func tuesday(n int) time.Time {
	return time.Date(2025, time.September, 2, 16, 0, 0, 0, time.UTC).AddDate(0, 0, 7*n)
}

// everything is a window wide enough for any case here.
var everything = Window{
	From: time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC),
	To:   time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
}
