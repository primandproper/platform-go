package metering

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/capitalism"
	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/metering/migrations"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// baseTime is the instant this suite works relative to. Deliberately mid-month
// and mid-day, so a period boundary is never coincidentally "now".
var baseTime = time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

// monthBounds is the calendar month baseTime falls in.
var monthBounds = Bounds{
	Start: time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
}

// dayBounds is the calendar day baseTime falls in.
var dayBounds = Bounds{
	Start: time.Date(2026, time.August, 15, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, time.August, 16, 0, 0, 0, 0, time.UTC),
}

// errArbitrary stands in for any failure a dependency can produce.
var errArbitrary = platformerrors.New("the dependency is unreachable")

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

// stubClock is a manually advanced clock. Periods, staleness budgets, and flush
// backoff are all functions of elapsed time and these tests need months of it, so
// they control the clock rather than race the wall.
//
// A synctest bubble would normally spare us a double, but it advances fake time
// only once every goroutine in the bubble is durably blocked, and these tests
// drive a real SQLite file. Built on the generated mock so the methods nothing
// calls fail loudly instead of lying.
type stubClock struct {
	*clockmock.ClockMock

	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*stubClock)(nil)

func newStubClock() *stubClock {
	c := &stubClock{now: baseTime}

	c.ClockMock = &clockmock.ClockMock{
		NowFunc:       c.read,
		SinceFunc:     func(t time.Time) time.Duration { return c.read().Sub(t) },
		NewTickerFunc: clock.NewClock().NewTicker,
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

// prefixCounter names a fresh table pair per subtest. Subtests share one database
// and must not share tables — the flush claim predicate is global to the totals
// table, so one test's backlog would be another's.
var prefixCounter atomic.Uint64

// storeEnv is one live database plus the dialect to emit SQL for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real SQL
// — placeholder rendering, the conflict clauses, the row-value IN lists, the
// partial indexes — without a container.
func newSQLiteEnv(tb testing.TB) *storeEnv {
	tb.Helper()

	client, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "metering.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// newStore migrates a uniquely prefixed table pair and returns a Store over it.
func (e *storeEnv) newStore(tb testing.TB) Store {
	tb.Helper()

	store, _ := e.newStoreWithPrefix(tb)

	return store
}

// newStoreWithPrefix is newStore, also handing back the prefix so a test can
// query the tables directly.
func (e *storeEnv) newStoreWithPrefix(tb testing.TB, opts ...SQLStoreOption) (store Store, prefix string) {
	tb.Helper()

	prefix = fmt.Sprintf("mtr_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err = NewSQLStore(e.client, append([]SQLStoreOption{WithTablePrefix(prefix)}, opts...)...)
	must.NoError(tb, err)

	return store, prefix
}

// newStoreWithLogger is newStore for the assertions about what a store writes to
// the log.
func (e *storeEnv) newStoreWithLogger(tb testing.TB, logger logging.Logger) Store {
	tb.Helper()

	store, _ := e.newStoreWithPrefix(tb, WithStoreLogger(logger))

	return store
}

// testMeter is the meter most of this suite counts.
const testMeter = "api_requests"

// testSubject is the account most of this suite is about.
const testSubject = "account-1"

// newTestRegistry builds a registry with one sum meter and one quota over it.
func newTestRegistry(tb testing.TB, behavior QuotaBehavior, limit int64) *Registry {
	tb.Helper()

	registry := NewRegistry()

	must.NoError(tb, registry.RegisterMeter(Meter{
		Name:        testMeter,
		Unit:        "requests",
		Aggregation: AggregationSum,
		Period:      PeriodMonth,
	}))
	must.NoError(tb, registry.RegisterQuota(Quota{
		Meter:    testMeter,
		Limit:    limit,
		Behavior: behavior,
		Period:   PeriodMonth,
	}))

	return registry
}

// newEntry builds an entry for the calendar month baseTime falls in.
func newEntry(key string, quantity int64, aggregation Aggregation) Entry {
	return newEntryAt(key, quantity, aggregation, baseTime)
}

// newEntryAt is newEntry with an explicit event time, for the ordering the last
// and max aggregations depend on.
func newEntryAt(key string, quantity int64, aggregation Aggregation, at time.Time) Entry {
	return Entry{
		Usage: Usage{
			Subject:        testSubject,
			Meter:          testMeter,
			Quantity:       quantity,
			IdempotencyKey: key,
			OccurredAt:     at,
		},
		Bounds:      monthBounds,
		Aggregation: aggregation,
	}
}

// stubCache is a cache.Cache whose expiry follows the stub clock.
//
// cache/memory reads the wall clock, so a staleness budget measured in seconds
// would either make these tests sleep or make them flaky. Only the four methods
// the enforcer uses are implemented; the rest report loudly rather than lying,
// because a silent no-op here would look like a cache that simply never hit.
type stubCache struct {
	clock   *stubClock
	entries map[string]stubCacheEntry

	mu sync.Mutex
}

type stubCacheEntry struct {
	expiresAt time.Time
	value     CachedTotal
}

var _ cache.Cache[CachedTotal] = (*stubCache)(nil)

func newStubCache(c *stubClock) *stubCache {
	return &stubCache{clock: c, entries: map[string]stubCacheEntry{}}
}

func (c *stubCache) Get(_ context.Context, key string) (*CachedTotal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, cache.ErrNotFound
	}

	if !entry.expiresAt.IsZero() && !c.clock.read().Before(entry.expiresAt) {
		delete(c.entries, key)

		return nil, cache.ErrNotFound
	}

	value := entry.value

	return &value, nil
}

func (c *stubCache) Set(_ context.Context, key string, value *CachedTotal, opts ...cache.WriteOption) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := stubCacheEntry{value: *value}
	if expiry := cache.EffectiveExpiry(0, opts...); expiry > 0 {
		entry.expiresAt = c.clock.read().Add(expiry)
	}

	c.entries[key] = entry

	return nil
}

func (c *stubCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, key)

	return nil
}

func (c *stubCache) GetMany(context.Context, []string) (map[string]*CachedTotal, error) {
	return nil, platformerrors.New("stub cache does not implement GetMany")
}

func (c *stubCache) SetIfPresent(context.Context, string, *CachedTotal, ...cache.WriteOption) error {
	return platformerrors.New("stub cache does not implement SetIfPresent")
}

func (c *stubCache) SetMany(context.Context, map[string]*CachedTotal, ...cache.WriteOption) error {
	return platformerrors.New("stub cache does not implement SetMany")
}

func (c *stubCache) DeleteMany(context.Context, []string) error {
	return platformerrors.New("stub cache does not implement DeleteMany")
}

func (c *stubCache) DeleteByPrefix(context.Context, string) error {
	return platformerrors.New("stub cache does not implement DeleteByPrefix")
}

func (c *stubCache) Flush(context.Context) error {
	return platformerrors.New("stub cache does not implement Flush")
}

func (c *stubCache) Ping(context.Context) error { return nil }

// recordingReporter is an in-process UsageReporter. It records what was posted so
// a test can assert the delta and the idempotency key, and can be made to fail or
// panic on demand.
type recordingReporter struct {
	err error

	posts []capitalism.UsageReportInput

	mu sync.Mutex

	panicNow bool
}

var _ capitalism.UsageReporter = (*recordingReporter)(nil)

func (r *recordingReporter) ReportUsage(_ context.Context, input *capitalism.UsageReportInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.panicNow {
		panic("the provider SDK dereferenced a nil pointer")
	}

	if r.err != nil {
		return r.err
	}

	r.posts = append(r.posts, *input)

	return nil
}

func (r *recordingReporter) recorded() []capitalism.UsageReportInput {
	r.mu.Lock()
	defer r.mu.Unlock()

	posts := make([]capitalism.UsageReportInput, len(r.posts))
	copy(posts, r.posts)

	return posts
}

// zeroMapper resolves every subject and meter to nothing at all, which the
// Flusher reads as "not billable" rather than as something to post.
func zeroMapper() ProviderMapper {
	return ProviderMapperFunc(func(context.Context, string, string) (ProviderRef, error) {
		return ProviderRef{}, nil
	})
}

// staticMapper resolves every subject and meter to one pair of provider handles.
func staticMapper(customerID string) ProviderMapper {
	return ProviderMapperFunc(func(_ context.Context, _, meter string) (ProviderRef, error) {
		return ProviderRef{CustomerID: customerID, MeterName: meter}, nil
	})
}

// failingTotalStore fails only the durable total read, so the enforcer's
// fail-open and fail-closed branches are both reachable without a broken
// database.
type failingTotalStore struct {
	Store
}

func (s *failingTotalStore) Total(context.Context, string, string, Bounds) (*Total, error) {
	return nil, errArbitrary
}

// failingConsumeStore fails only Consume.
type failingConsumeStore struct {
	Store
}

func (s *failingConsumeStore) Consume(context.Context, Entry, int64, QuotaBehavior, time.Time) (*Decision, error) {
	return nil, errArbitrary
}

// failingClaimStore fails every flush claim, so a pass's error path is reachable.
// Embedding the real Store means only the one method under test is a double.
type failingClaimStore struct {
	Store
}

func (s *failingClaimStore) ClaimFlushable(context.Context, time.Time, int, int, time.Time) ([]*Total, error) {
	return nil, errArbitrary
}

// failingReapStore fails only the retention reap, so a pass's other chores still
// run and the partial result is observable.
type failingReapStore struct {
	Store
}

func (s *failingReapStore) ReapEvents(context.Context, time.Time, int) (int64, error) {
	return 0, errArbitrary
}

// failingSettleStore fails only MarkFlushed, so the "provider has it and the row
// does not say so" path is reachable.
type failingSettleStore struct {
	Store
}

func (s *failingSettleStore) MarkFlushed(context.Context, *Total, int64, time.Time) error {
	return errArbitrary
}

// failingReleaseStore fails only ReleaseFlush, so the path where the lease is
// left to expire is reachable.
type failingReleaseStore struct {
	Store
}

func (s *failingReleaseStore) ReleaseFlush(context.Context, *Total, string, time.Time) error {
	return errArbitrary
}

// recordFailingStore fails every durable record, so a recorder's error path is
// reachable.
type recordFailingStore struct {
	Store
}

func (s *recordFailingStore) Record(context.Context, []Entry, time.Time) (RecordResult, error) {
	return RecordResult{}, errArbitrary
}

func (s *recordFailingStore) RecordTx(
	context.Context,
	database.SQLQueryExecutor,
	[]Entry,
	time.Time,
) (RecordResult, error) {
	return RecordResult{}, errArbitrary
}

// countRows counts rows in one of a store's tables.
func countRows(t *testing.T, env *storeEnv, table string) int {
	t.Helper()

	var count int
	must.NoError(t, env.client.Reader().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count))

	return count
}

// Close satisfies cache.Cache.
func (s *stubCache) Close() error { return nil }

// recordingInstruments keeps every measurement the component under test made,
// keyed by instrument name. Nothing in this package asserted a measurement
// before: an instrument that was never fed, or fed the wrong number, looked
// exactly like one working — and these counters are what a metering deployment
// is watched by, so "the code ran" is not the question worth answering about
// them.
type recordingInstruments struct {
	values map[string][]int64
	mu     sync.Mutex
}

func newRecordingInstruments() *recordingInstruments {
	return &recordingInstruments{values: map[string][]int64{}}
}

// provider hands out instruments that record into i. Everything this package
// builds and does not measure here — the histograms — is satisfied with a
// discard.
func (i *recordingInstruments) provider() metrics.Provider {
	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			return &recordingInstrument{into: i, name: name}, nil
		},
		NewInt64GaugeFunc: func(name string, _ ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
			return &recordingInstrument{into: i, name: name}, nil
		},
		NewFloat64HistogramFunc: func(string, ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
			return &discardHistogram{}, nil
		},
	}
}

// recorded returns the measurements made on one instrument, in order. The name
// is the suffix the component appends to its service name, so a test names
// "_flushes" rather than repeating the prefix.
func (i *recordingInstruments) recorded(suffix string) []int64 {
	i.mu.Lock()
	defer i.mu.Unlock()

	for name, values := range i.values {
		if strings.HasSuffix(name, suffix) {
			return append([]int64(nil), values...)
		}
	}

	return nil
}

func (i *recordingInstruments) record(name string, value int64) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.values[name] = append(i.values[name], value)
}

// recordingInstrument is both an Int64Counter and an Int64Gauge, which are the
// same shape as far as a test that only wants the numbers is concerned.
type recordingInstrument struct {
	into *recordingInstruments
	name string
}

func (c *recordingInstrument) Add(_ context.Context, incr int64, _ ...metric.AddOption) {
	c.into.record(c.name, incr)
}

func (c *recordingInstrument) Record(_ context.Context, value int64, _ ...metric.RecordOption) {
	c.into.record(c.name, value)
}

type discardHistogram struct{}

func (*discardHistogram) Record(context.Context, float64, ...metric.RecordOption) {}

// loggedLine is one message a component wrote, and the values its logger
// carried when it did.
type loggedLine struct {
	err     error
	values  map[string]any
	message string
	level   logging.Level
}

// recordingLogger keeps what it was told, so a test can assert on a line a
// component writes and nothing else observes — the guard-miss line, the
// backlog warning, the analytics failure that is deliberately swallowed.
//
// Derived loggers share the root's slice: Operation.Set replaces its logger
// with a derived one for every value it records, so what a test wants to see is
// everything that reached the root.
type recordingLogger struct {
	lines  *[]loggedLine
	values map[string]any
	mu     *sync.Mutex
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{lines: &[]loggedLine{}, values: map[string]any{}, mu: &sync.Mutex{}}
}

// at returns the lines recorded at one level.
func (l *recordingLogger) at(level logging.Level) []loggedLine {
	l.mu.Lock()
	defer l.mu.Unlock()

	var found []loggedLine

	for i := range *l.lines {
		if recorded := (*l.lines)[i]; recorded.level == level {
			found = append(found, recorded)
		}
	}

	return found
}

// messages returns the messages recorded at one level.
func (l *recordingLogger) messages(level logging.Level) []string {
	lines := l.at(level)

	messages := make([]string, 0, len(lines))
	for i := range lines {
		messages = append(messages, lines[i].message)
	}

	return messages
}

func (l *recordingLogger) record(level logging.Level, message string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	*l.lines = append(*l.lines, loggedLine{
		level:   level,
		message: message,
		err:     err,
		values:  maps.Clone(l.values),
	})
}

func (l *recordingLogger) with(values map[string]any) logging.Logger {
	merged := make(map[string]any, len(l.values)+len(values))
	maps.Copy(merged, l.values)
	maps.Copy(merged, values)

	return &recordingLogger{lines: l.lines, values: merged, mu: l.mu}
}

func (l *recordingLogger) Info(message string)  { l.record(logging.InfoLevel, message, nil) }
func (l *recordingLogger) Debug(message string) { l.record(logging.DebugLevel, message, nil) }
func (l *recordingLogger) Warn(message string)  { l.record(logging.WarnLevel, message, nil) }

func (l *recordingLogger) Error(message string, err error) {
	l.record(logging.ErrorLevel, message, err)
}

func (l *recordingLogger) WithValue(key string, value any) logging.Logger {
	return l.with(map[string]any{key: value})
}

func (l *recordingLogger) WithValues(values map[string]any) logging.Logger { return l.with(values) }

func (l *recordingLogger) SetRequestIDFunc(logging.RequestIDFunc)     {}
func (l *recordingLogger) Clone() logging.Logger                      { return l.with(nil) }
func (l *recordingLogger) WithName(string) logging.Logger             { return l.with(nil) }
func (l *recordingLogger) WithRequest(*http.Request) logging.Logger   { return l.with(nil) }
func (l *recordingLogger) WithResponse(*http.Response) logging.Logger { return l.with(nil) }
func (l *recordingLogger) WithError(error) logging.Logger             { return l.with(nil) }
func (l *recordingLogger) WithSpan(trace.Span) logging.Logger         { return l.with(nil) }
