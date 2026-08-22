package shredding

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/aes"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/kms/local"
	"github.com/primandproper/platform-go/v13/cryptography/shredding/migrations"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

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

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "shredding.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// newStore migrates a uniquely prefixed key table and returns a Store over it.
func (e *storeEnv) newStore(t *testing.T) Store {
	t.Helper()

	prefix := fmt.Sprintf("sh_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := NewSQLStore(e.client, WithTablePrefix(prefix))
	must.NoError(t, err)

	return store
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
