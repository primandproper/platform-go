package saga

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/saga/internal/queries"
	"github.com/primandproper/platform-go/v14/saga/migrations"

	cachememory "github.com/primandproper/primitives-go/v2/cache/memory"
	"github.com/primandproper/primitives-go/v2/clock"
	clockmock "github.com/primandproper/primitives-go/v2/clock/mock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/distributedlock"
	lockmemory "github.com/primandproper/primitives-go/v2/distributedlock/memory"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/idempotency"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	metricsmock "github.com/primandproper/primitives-go/v2/observability/metrics/mock"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// baseTime is the instant this suite works relative to.
var baseTime = time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)

// testState is the state type most of this suite's definitions carry.
type testState struct {
	Trail  []string `json:"trail"`
	Amount int      `json:"amount"`
}

// otherState exists solely so a Runner of the wrong type has something to be
// the wrong type of.
type otherState struct {
	Name string `json:"name"`
}

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

// stubClock is a manually advanced clock. Backoff, delays, and leases are all
// functions of elapsed time and these tests need minutes of it, so they control
// the clock rather than race the wall.
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
		SleepFunc:     clock.NewClock().Sleep,
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

// prefixCounter names a fresh table per subtest. Subtests share one database
// and must not share tables — the claim predicate is global to the instance
// table, so one test's backlog would be another's.
var prefixCounter atomic.Uint64

// storeEnv is one live database plus the dialect to emit SQL for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real
// SQL — placeholder rendering, the claim predicate, the guarded advances, the
// partial index — without a container.
func newSQLiteEnv(t *testing.T) *storeEnv {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "saga.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// newStore migrates a uniquely prefixed instance table and returns a Store over
// it.
func (e *storeEnv) newStore(t *testing.T) Store {
	t.Helper()

	prefix := fmt.Sprintf("sg_%d", prefixCounter.Add(1))

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

// instancesTable names the table a store's statements run against, for the
// tests that reach past the store to set a row up or to corrupt one.
//
// It is assembled the way the store assembles it — the configured prefix,
// qualified, in front of the canonical name saga/internal/queries spells — so a
// test cannot come to name a different table than the one under test.
func instancesTable(t *testing.T, store Store) string {
	t.Helper()

	concrete, ok := store.(*SQLStore)
	must.True(t, ok)

	return ddl.Qualify(concrete.prefix) + queries.InstancesTable
}

// newScopedLocker builds an in-process scoped locker, which is all a
// single-process test needs from distributedlock.
func newScopedLocker(t *testing.T) distributedlock.ScopedLocker {
	t.Helper()

	raw, err := lockmemory.NewLocker()
	must.NoError(t, err)

	scoped, err := distributedlock.NewScopedLocker(raw)
	must.NoError(t, err)

	return scoped
}

// newIdempotencyManager builds a manager over an in-memory record store, so the
// replay path is exercised without a Redis.
func newIdempotencyManager(t *testing.T) *idempotency.Manager[StepResult] {
	t.Helper()

	records, err := cachememory.NewInMemoryCache[idempotency.Record[StepResult]](time.Hour)
	must.NoError(t, err)

	manager, err := idempotency.NewManager(records, newScopedLocker(t),
		idempotency.WithInFlightTTL(time.Minute))
	must.NoError(t, err)

	return manager
}

// saveInstance inserts an instance through a transaction, as a Runner does.
//
// The transaction comes from the environment's client rather than from the
// store, because a Store has no WithTransaction: one way in, and it is
// database.Client's.
func (e *storeEnv) saveInstance(t *testing.T, store Store, inst *Record, nextAttempt time.Time) *Record {
	t.Helper()

	must.NoError(t, e.client.WithTransaction(t.Context(), func(q database.Tx) error {
		return store.Save(t.Context(), q, inst, nextAttempt)
	}))

	return inst
}

// newRecord builds a running instance at step zero.
func newRecord(id, definitionName string, stepNames []string, state any, at time.Time) *Record {
	encoded, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}

	return &Record{
		CreatedAt:   at,
		State:       encoded,
		StepNames:   stepNames,
		ID:          id,
		Definition:  definitionName,
		Status:      StatusRunning,
		CurrentStep: 0,
	}
}

// recorder collects what a definition's steps did, in order, so a test can
// assert the sequence rather than only the end state.
type recorder struct {
	calls []string
	mu    sync.Mutex
}

func (r *recorder) record(what string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, what)
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.calls...)
}

// trailStep builds a step that appends its own name to the state's trail and
// records that it ran.
func trailStep(rec *recorder, name string, doErr, undoErr error) Step[testState] {
	step := Step[testState]{
		Name: name,
		Do: func(_ context.Context, s *testState) error {
			rec.record("do:" + name)
			s.Trail = append(s.Trail, "do:"+name)

			return doErr
		},
		Undo: func(_ context.Context, s *testState) error {
			rec.record("undo:" + name)
			s.Trail = append(s.Trail, "undo:"+name)

			return undoErr
		},
	}

	return step
}

// testWorkerConfig is a config whose timings suit a test: no real waiting, and
// budgets small enough that exhaustion is reachable in a handful of passes.
func testWorkerConfig() *WorkerConfig {
	cfg := &WorkerConfig{}
	cfg.EnsureDefaults()

	cfg.PollInterval = time.Millisecond
	cfg.Backoff.MaxAttempts = 2
	cfg.Backoff.InitialDelay = time.Millisecond
	cfg.Backoff.MaxDelay = time.Millisecond
	cfg.CompensationBackoff.MaxAttempts = 2
	cfg.CompensationBackoff.InitialDelay = time.Millisecond
	cfg.CompensationBackoff.MaxDelay = time.Millisecond

	return cfg
}

// newWorker builds a Worker over the given store and registry with the suite's
// clock, plus whatever extra options a test needs.
func (e *storeEnv) newWorker(t *testing.T, store Store, registry *Registry, c clock.Clock, opts ...WorkerOption) *Worker {
	t.Helper()

	worker, err := NewWorker(t.Context(), testWorkerConfig(), e.client, store, registry, newScopedLocker(t),
		append([]WorkerOption{WithWorkerClock(c)}, opts...)...)
	must.NoError(t, err)

	return worker
}

// drainOnce runs exactly one worker cycle, which is what a test wants instead
// of a background goroutine it then has to synchronize with.
func drainOnce(t *testing.T, w *Worker) {
	t.Helper()

	w.cycle(t.Context())
}

// drain runs cycles until the instance reaches a terminal status or the budget
// runs out, advancing the clock between passes so scheduled retries come due.
func drain(t *testing.T, w *Worker, store Store, c *stubClock, id string, maxCycles int) *Record {
	t.Helper()

	var inst *Record

	for range maxCycles {
		drainOnce(t, w)

		var err error

		inst, err = store.Get(t.Context(), id)
		must.NoError(t, err)

		if inst.Status.Terminal() {
			return inst
		}

		c.advance(time.Minute)
	}

	return inst
}

// failingClaimStore fails every Claim, so a cycle's error path is reachable.
// Embedding the real Store means only the one method under test is a double.
type failingClaimStore struct {
	Store
}

func (s *failingClaimStore) Claim(context.Context, time.Time, int, time.Time) ([]*Record, error) {
	return nil, platformerrors.New("the database is unreachable")
}

// failingReleaseStore fails only Release, so the worker's best-effort release
// logging is reachable.
type failingReleaseStore struct {
	Store
}

func (s *failingReleaseStore) Release(context.Context, string, time.Time) error {
	return platformerrors.New("the write replica is unreachable")
}

// failingAdvanceStore fails every Advance, so the path where a step succeeded
// and its progress could not be recorded is reachable.
type failingAdvanceStore struct {
	Store
}

func (s *failingAdvanceStore) Advance(context.Context, database.Tx, *Record, time.Time, time.Time) error {
	return platformerrors.New("the write replica is unreachable")
}

// heldLocker reports every key as already held, so the contended path is
// reachable without a second worker.
type heldLocker struct{}

var _ distributedlock.ScopedLocker = (*heldLocker)(nil)

func (heldLocker) WithLock(context.Context, string, func(context.Context) error) error {
	return platformerrors.New("not used")
}

func (heldLocker) TryWithLock(context.Context, string, func(context.Context) error) (bool, error) {
	return false, nil
}

// recordingInstruments keeps every measurement the component under test made,
// keyed by instrument name.
//
// The suite asserts that instruments exist — see instruments_test.go — and,
// for the stuck level, what they were fed. A gauge fed the wrong number looks
// exactly like one working, and this is the number an operator is woken by.
type recordingInstruments struct {
	values map[string][]int64
	mu     sync.Mutex
}

func newRecordingInstruments() *recordingInstruments {
	return &recordingInstruments{values: map[string][]int64{}}
}

// provider hands out instruments that record into i. The histograms are
// discarded: a latency is not a number this suite has anything to say about.
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
// is the suffix this package appends to its service name, so a test names
// "_instances_stuck_depth" rather than repeating the prefix.
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

// failingListStore fails every List, so the stats read's error path is
// reachable.
type failingListStore struct {
	Store
}

func (s *failingListStore) List(
	context.Context,
	*ListScope,
	*filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Record], error) {
	return nil, platformerrors.New("the read replica is unreachable")
}

// stuckRecord saves an instance already in StatusStuck, as a worker that ran
// out of compensation attempts would have left it.
func (e *storeEnv) stuckRecord(t *testing.T, store Store, definitionName, id string) *Record {
	t.Helper()

	inst := newRecord(id, definitionName, []string{"one"}, testState{}, baseTime)
	inst.Status = StatusStuck
	inst.ResumeStatus = StatusCompensating

	return e.saveInstance(t, store, inst, baseTime)
}
