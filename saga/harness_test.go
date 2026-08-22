package saga

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cachememory "github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/distributedlock"
	lockmemory "github.com/primandproper/platform-go/v13/distributedlock/memory"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/saga/migrations"

	"github.com/shoenig/test/must"
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
func saveInstance(t *testing.T, store Store, inst *Record, nextAttempt time.Time) *Record {
	t.Helper()

	must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
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
		StartedAt:   at,
		UpdatedAt:   at,
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
func newWorker(t *testing.T, store Store, registry *Registry, c clock.Clock, opts ...WorkerOption) *Worker {
	t.Helper()

	worker, err := NewWorker(t.Context(), testWorkerConfig(), store, registry, newScopedLocker(t),
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

func (s *failingAdvanceStore) Advance(context.Context, database.SQLQueryExecutor, *Record, time.Time) error {
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
