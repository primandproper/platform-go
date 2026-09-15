package shredding

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/shredding/internal/queries"
	"github.com/primandproper/platform-go/v14/shredding/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	clockmock "github.com/primandproper/primitives-go/v2/clock/mock"
	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
	"github.com/primandproper/primitives-go/v2/cryptography/encryption/aes"
	"github.com/primandproper/primitives-go/v2/cryptography/encryption/kms/local"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	metricsmock "github.com/primandproper/primitives-go/v2/observability/metrics/mock"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// allDialects is every dialect this package serves. The interesting failures
// are the ones that are correct on two of the three.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// baseTime is the instant this suite works relative to.
var baseTime = time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)

// testSubject is the subject most of these tests are about.
var testSubject = Subject{Type: "user", ID: "user-1"}

// rootKeyMaterial is the root key the local wrapper wraps with. Thirty-two
// bytes, and constant, because a test that generated one would be testing
// crypto/rand.
var rootKeyMaterial = []byte("0123456789abcdef0123456789abcdef")

// prefixCounter gives each subtest its own table, so tests that count rows or
// race two writers do not see each other's subjects.
var prefixCounter atomic.Uint64

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

// stubClock is a manually advanced clock. The cache TTL is the guarantee this
// package makes, and testing a guarantee by sleeping for it is how a suite comes
// to take five minutes.
type stubClock struct {
	*clockmock.ClockMock

	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*stubClock)(nil)

func newStubClock() *stubClock {
	c := &stubClock{now: baseTime}

	c.ClockMock = &clockmock.ClockMock{
		NowFunc:   c.read,
		SinceFunc: func(t time.Time) time.Duration { return c.read().Sub(t) },
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

// storeEnv is a database and the dialect it speaks.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real
// SQL — placeholder rendering, the insert-ignore clause, the guarded update —
// without a container.
func newSQLiteEnv(t *testing.T) *storeEnv {
	t.Helper()

	return &storeEnv{client: newSQLiteClient(t), dialect: dialect.SQLite}
}

// newSQLiteClient opens one SQLite database, in a directory of its own so that
// two of them are two databases rather than two handles on one.
func newSQLiteClient(t *testing.T) database.Client {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "shredding.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// migrateKeysTable renders the keys table under prefix and applies it.
func migrateKeysTable(t *testing.T, client database.Client, d dialect.Dialect, prefix string) {
	t.Helper()

	stmts, err := migrations.Statements(d, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}
}

// newStore migrates a uniquely prefixed key table and returns a Store over it.
func (e *storeEnv) newStore(t *testing.T) Store {
	t.Helper()

	store, _ := e.newPrefixedStore(t)

	return store
}

// newPrefixedStore is newStore for the tests that read a column back through
// their own SELECT — which needs the table's rendered name, and therefore the
// prefix the store was built with.
func (e *storeEnv) newPrefixedStore(t *testing.T) (store Store, prefix string) {
	t.Helper()

	prefix = newTablePrefix()

	migrateKeysTable(t, e.client, e.dialect, prefix)

	store, err := NewSQLStore(e.client, WithTablePrefix(prefix))
	must.NoError(t, err)

	return store, prefix
}

// newTablePrefix names a table no other subtest is using.
func newTablePrefix() string {
	return fmt.Sprintf("sh_%d", prefixCounter.Add(1))
}

// laggingClient is a database.Client whose reads answer from a second database.
//
// A read replica standing behind its primary is not reproducible on one
// connection — every reader of a SQLite file sees every committed write — so
// the replica here is its own database, and it only ever holds what a test put
// there. That makes the lag total rather than timed, which is the only version
// of it a test can assert on.
type laggingClient struct {
	database.Client

	replica database.Client
}

var _ database.Client = (*laggingClient)(nil)

func (c *laggingClient) Reader() database.SQLQueryExecutor { return c.replica.Reader() }

// laggingEnv is the store under test and a way to see each of its two
// databases on its own.
type laggingEnv struct {
	// store is the subject of these tests: it writes to the primary and reads
	// through the replica.
	store *SQLStore
	// primary reads and writes the write database directly, which is how a test
	// establishes what is actually true without going through the seam it is
	// testing.
	primary *SQLStore
	// replica writes the read database directly, which is how a test says how
	// far behind the read side is.
	replica *SQLStore
}

// newLaggingEnv builds a store whose writes land on a primary and whose
// Reader() answers from a replica that only holds what a test put there.
//
// Both databases carry the same table under the same prefix, because a replica
// with a different schema would fail for a reason that is not the one under
// test.
func newLaggingEnv(t *testing.T) *laggingEnv {
	t.Helper()

	prefix := newTablePrefix()

	primary, replica := newSQLiteClient(t), newSQLiteClient(t)

	migrateKeysTable(t, primary, dialect.SQLite, prefix)
	migrateKeysTable(t, replica, dialect.SQLite, prefix)

	env := &laggingEnv{}

	var err error

	env.store, err = NewSQLStore(&laggingClient{Client: primary, replica: replica}, WithTablePrefix(prefix))
	must.NoError(t, err)

	env.primary, err = NewSQLStore(primary, WithTablePrefix(prefix))
	must.NoError(t, err)

	env.replica, err = NewSQLStore(replica, WithTablePrefix(prefix))
	must.NoError(t, err)

	return env
}

// catchUp copies the subject's row from the primary to the replica, leaving the
// read side holding exactly what is true now and nothing that happens after.
func (e *laggingEnv) catchUp(t *testing.T, subject Subject) {
	t.Helper()

	record, err := e.primary.Load(t.Context(), subject)
	must.NoError(t, err)

	inserted, err := e.replica.Insert(t.Context(), record)
	must.NoError(t, err)
	must.True(t, inserted)
}

// keysTable renders the table name a prefixed store writes to, for the reads
// those tests issue directly.
func keysTable(prefix string) string {
	return ddl.Qualify(prefix) + queries.SubjectKeysTable
}

// newTestWrapper builds the local key wrapper these tests wrap data keys with.
func newTestWrapper(t *testing.T) encryption.KeyWrapper {
	t.Helper()

	cipher, err := aes.NewCipher(rootKeyMaterial)
	must.NoError(t, err)

	wrapper, err := local.NewKeyWrapper(cipher)
	must.NoError(t, err)

	return wrapper
}

// newTestKeys builds a Keys over a real SQLite store and a real local wrapper.
//
// Real ones on purpose: the properties under test — that a shredded key cannot
// be brought back, that two minters agree on one key — are properties of the
// store and the wrapper working together, and a pair of mocks would only assert
// that this package calls them.
func newTestKeys(t *testing.T, c clock.Clock, opts ...Option) (Keys, Store) {
	t.Helper()

	store := newSQLiteEnv(t).newStore(t)

	keys, err := NewKeys(store, newTestWrapper(t), append([]Option{WithClock(c)}, opts...)...)
	must.NoError(t, err)

	return keys, store
}

// newTestCipher builds a Cipher over arbitrary key material, for the cache
// tests that only need something non-nil to hold.
func newTestCipher() (encryption.Cipher, error) {
	return aes.NewCipher(rootKeyMaterial)
}

// countingMeter records what a component reported: how many times each
// instrument was incremented, and with which attributes.
//
// Asserting that a component did the work and trusting that it said so is
// exactly the gap these instruments exist to close, so the tests that care read
// the instruments instead.
type countingMeter struct {
	*metricsmock.ProviderMock

	counts map[string]int64
	attrs  map[string][]attribute.Set
	mu     sync.Mutex
}

func newCountingMeter() *countingMeter {
	m := &countingMeter{
		counts: map[string]int64{},
		attrs:  map[string][]attribute.Set{},
	}

	noop := metrics.EnsureMetricsProvider(nil)

	m.ProviderMock = &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			return &metricsmock.Int64CounterMock{
				AddFunc: func(_ context.Context, incr int64, options ...metric.AddOption) {
					m.record(name, incr, options)
				},
			}, nil
		},
		NewInt64GaugeFunc: noop.NewInt64Gauge,
	}

	return m
}

func (m *countingMeter) record(name string, incr int64, options []metric.AddOption) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.counts[name] += incr
	m.attrs[name] = append(m.attrs[name], metric.NewAddConfig(options).Attributes())
}

// count reports the total an instrument was incremented by.
func (m *countingMeter) count(name string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.counts[name]
}

// countWhere reports how many increments of an instrument carried a boolean
// attribute with the given value.
func (m *countingMeter) countWhere(name, key string, value bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	var total int

	for _, set := range m.attrs[name] {
		if v, ok := set.Value(attribute.Key(key)); ok && v.AsBool() == value {
			total++
		}
	}

	return total
}

// recordingInvalidator captures what a handler dropped.
type recordingInvalidator struct {
	subjects []Subject
	mu       sync.Mutex
}

var _ Invalidator = (*recordingInvalidator)(nil)

func (r *recordingInvalidator) Invalidate(_ context.Context, subject Subject) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.subjects = append(r.subjects, subject)
}

func (r *recordingInvalidator) seen() []Subject {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]Subject(nil), r.subjects...)
}

// recordingBroadcaster captures what a shred announced.
type recordingBroadcaster struct {
	err      error
	subjects []Subject
	mu       sync.Mutex
}

var _ Broadcaster = (*recordingBroadcaster)(nil)

func (b *recordingBroadcaster) Broadcast(_ context.Context, subject Subject) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.subjects = append(b.subjects, subject)

	return b.err
}

func (b *recordingBroadcaster) seen() []Subject {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]Subject(nil), b.subjects...)
}
