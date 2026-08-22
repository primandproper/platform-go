package idempotency

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	cachememory "github.com/primandproper/platform-go/v13/cache/memory"
	cachemock "github.com/primandproper/platform-go/v13/cache/mock"
	"github.com/primandproper/platform-go/v13/distributedlock"
	dlmemory "github.com/primandproper/platform-go/v13/distributedlock/memory"
	distributedlockmock "github.com/primandproper/platform-go/v13/distributedlock/mock"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

const (
	testKey         = "test-key"
	testFingerprint = "test-fingerprint"
)

// payload is the recorded value in these tests. It is a concrete struct with
// exported fields, which is what the store can round-trip.
type payload struct {
	Name string
}

// newStore builds an in-memory record store. The expiry is per-write, so the
// cache default is irrelevant.
func newStore(tb testing.TB) cache.Cache[Record[payload]] {
	tb.Helper()

	c, err := cachememory.NewInMemoryCache[Record[payload]](0)
	must.NoError(tb, err)

	return c
}

// newLocker builds a real in-process scoped locker, so concurrency tests
// exercise actual mutual exclusion rather than a mock that always grants.
func newLocker(tb testing.TB) distributedlock.ScopedLocker {
	tb.Helper()

	locker, err := dlmemory.NewLocker()
	must.NoError(tb, err)

	scoped, err := distributedlock.NewScopedLocker(locker)
	must.NoError(tb, err)

	return scoped
}

// newTestManager builds a Manager over a memory store and a memory locker.
func newTestManager(tb testing.TB, opts ...Option) *Manager[payload] {
	tb.Helper()

	m, err := NewManager(newStore(tb), newLocker(tb), opts...)
	must.NoError(tb, err)

	return m
}

// countingFn records how many times the work ran, so a replay can be
// distinguished from a re-execution by something other than its result.
type countingFn struct {
	value *payload
	err   error
	calls int64
	mu    sync.Mutex
}

func newCountingFn(name string) *countingFn {
	return &countingFn{value: &payload{Name: name}}
}

func (f *countingFn) run(context.Context) (*payload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	return f.value, f.err
}

func (f *countingFn) Calls() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

// countingCounter records what an instrument was asked to add, so a test can
// assert a counter fired without standing up an SDK metrics pipeline.
type countingCounter struct {
	mu    sync.Mutex
	total int64
}

func (c *countingCounter) Add(_ context.Context, incr int64, _ ...metric.AddOption) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.total += incr
}

func (c *countingCounter) Total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.total
}

// failingStore wraps a real store and fails Get with err.
//
// It is built on the generated mock so that any method a test did not expect
// to be called panics rather than silently returning a zero value.
func failingStore(tb testing.TB, err error) *cachemock.CacheMock[Record[payload]] {
	tb.Helper()

	return &cachemock.CacheMock[Record[payload]]{
		GetFunc: func(context.Context, string) (*Record[payload], error) {
			return nil, err
		},
	}
}

// grantingLocker runs fn without any locking, optionally doing something first.
//
// before is where a test injects a racer: whatever it writes to the store lands
// between Do's pre-lock read and the claim, which is the interleaving the
// double-check exists to survive.
func grantingLocker(before func(ctx context.Context)) *distributedlockmock.ScopedLockerMock {
	return &distributedlockmock.ScopedLockerMock{
		WithLockFunc: func(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
			if before != nil {
				before(ctx)
			}

			return fn(ctx)
		},
	}
}

// countingStore wraps a real store and lets a test fail a specific call.
//
// Failing the nth call rather than all of them is what makes the interesting
// paths reachable: the claim's re-read, the completion write, and the release
// are each a later call against a store whose earlier calls must succeed.
type countingStore struct {
	inner cache.Cache[Record[payload]]

	getErr    error
	setErr    error
	deleteErr error

	failGetAfter    int
	failSetAfter    int
	failDeleteAfter int

	gets    int
	sets    int
	deletes int
	mu      sync.Mutex
}

var _ cache.Cache[Record[payload]] = (*countingStore)(nil)

func newCountingStore(tb testing.TB) *countingStore {
	tb.Helper()

	return &countingStore{inner: newStore(tb), failGetAfter: -1, failSetAfter: -1, failDeleteAfter: -1}
}

// shouldFail reports whether this call number is the one to fail.
func shouldFail(n, failAfter int, err error) bool {
	return err != nil && failAfter >= 0 && n > failAfter
}

func (s *countingStore) Get(ctx context.Context, key string) (*Record[payload], error) {
	s.mu.Lock()
	s.gets++
	n := s.gets
	s.mu.Unlock()

	if shouldFail(n, s.failGetAfter, s.getErr) {
		return nil, s.getErr
	}

	return s.inner.Get(ctx, key)
}

func (s *countingStore) Set(ctx context.Context, key string, value *Record[payload], opts ...cache.WriteOption) error {
	s.mu.Lock()
	s.sets++
	n := s.sets
	s.mu.Unlock()

	if shouldFail(n, s.failSetAfter, s.setErr) {
		return s.setErr
	}

	return s.inner.Set(ctx, key, value, opts...)
}

// SetIfPresent counts against the same budget as Set, because to these tests it
// is a write like any other: what they fail is the nth attempt to store
// something, and which flavor of store it was is not what they are varying.
func (s *countingStore) SetIfPresent(
	ctx context.Context,
	key string,
	value *Record[payload],
	opts ...cache.WriteOption,
) error {
	s.mu.Lock()
	s.sets++
	n := s.sets
	s.mu.Unlock()

	if shouldFail(n, s.failSetAfter, s.setErr) {
		return s.setErr
	}

	return s.inner.SetIfPresent(ctx, key, value, opts...)
}

func (s *countingStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	s.deletes++
	n := s.deletes
	s.mu.Unlock()

	if shouldFail(n, s.failDeleteAfter, s.deleteErr) {
		return s.deleteErr
	}

	return s.inner.Delete(ctx, key)
}

func (s *countingStore) GetMany(ctx context.Context, keys []string) (map[string]*Record[payload], error) {
	return s.inner.GetMany(ctx, keys)
}

func (s *countingStore) SetMany(ctx context.Context, items map[string]*Record[payload], opts ...cache.WriteOption) error {
	return s.inner.SetMany(ctx, items, opts...)
}

func (s *countingStore) DeleteMany(ctx context.Context, keys []string) error {
	return s.inner.DeleteMany(ctx, keys)
}

func (s *countingStore) DeleteByPrefix(ctx context.Context, prefix string) error {
	return s.inner.DeleteByPrefix(ctx, prefix)
}

func (s *countingStore) Flush(ctx context.Context) error { return s.inner.Flush(ctx) }
func (s *countingStore) Ping(ctx context.Context) error  { return s.inner.Ping(ctx) }

// newManagerOver builds a Manager over a specific store.
func newManagerOver(
	tb testing.TB,
	store cache.Cache[Record[payload]],
	opts ...Option,
) *Manager[payload] {
	tb.Helper()

	m, err := NewManager(store, newLocker(tb), opts...)
	must.NoError(tb, err)

	return m
}

// seed writes a record directly, standing in for whatever another process left
// behind.
func seed(t *testing.T, m *Manager[payload], key Key, record *Record[payload], expiry time.Duration) {
	t.Helper()

	must.NoError(t, m.store.Set(t.Context(), m.storeKey(key), record, cache.WithExpiry(expiry)))
}

// completed builds a finished record for fingerprint.
func completed(fingerprint Fingerprint, name string) *Record[payload] {
	return &Record[payload]{
		CreatedAt:   time.Now().UTC(),
		Value:       &payload{Name: name},
		Fingerprint: fingerprint,
		ClaimID:     "seeded",
		Version:     recordVersion,
		State:       StateCompleted,
	}
}

// inFlight builds a claim record for fingerprint.
func inFlight(fingerprint Fingerprint) *Record[payload] {
	return &Record[payload]{
		CreatedAt:   time.Now().UTC(),
		Fingerprint: fingerprint,
		ClaimID:     "seeded",
		Version:     recordVersion,
		State:       StateInFlight,
	}
}

// Close satisfies cache.Cache.
func (s *countingStore) Close() error { return nil }
