package sessions

import (
	"context"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"

	"github.com/shoenig/test/must"
)

// principal is the payload the tests store.
type principal struct {
	UserID string
	Admin  bool
}

// fakeClock is a Clock whose time only moves when a test moves it.
type fakeClock struct {
	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*fakeClock)(nil)

func newFakeClock() *fakeClock {
	// A fixed instant with a sub-microsecond tail, so that the store's
	// truncation is exercised rather than accidentally satisfied.
	return &fakeClock{now: time.Date(2026, time.August, 8, 12, 0, 0, 123456789, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

func (c *fakeClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func (c *fakeClock) NewTicker(_ time.Duration) clock.Ticker { panic("not used") }

// advance moves the clock forward.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// entry is one record in the fake backend, with the deadline it was written
// under.
type entry[T any] struct {
	record    *Record[T]
	expiresAt time.Time
}

// fakeBackend is an in-memory Backend that honors the contract exactly: Create
// refuses a duplicate, Update refuses an absent identifier, Rename is atomic.
//
// It is hand-rolled rather than borrowed from sessions/cache because that
// package imports this one, and because these tests are about the Store's
// logic rather than about any particular storage.
type fakeBackend[T any] struct {
	entries map[string]entry[T]
	clock   *fakeClock
	calls   map[string]int

	// failures, when set for an operation name, is what that operation returns
	// instead of doing its work.
	failures map[string]error

	mu     sync.Mutex
	closed bool
}

var _ Backend[principal] = (*fakeBackend[principal])(nil)

func newFakeBackend[T any](c *fakeClock) *fakeBackend[T] {
	return &fakeBackend[T]{
		entries:  map[string]entry[T]{},
		clock:    c,
		failures: map[string]error{},
		calls:    map[string]int{},
	}
}

// fail makes the named operation return err until it is unset.
func (b *fakeBackend[T]) fail(operation string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.failures[operation] = err
}

func (b *fakeBackend[T]) callCount(operation string) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.calls[operation]
}

// enter records the call and returns the configured failure, if any.
func (b *fakeBackend[T]) enter(operation string) error {
	b.calls[operation]++

	return b.failures[operation]
}

func (b *fakeBackend[T]) Load(_ context.Context, id string) (*Record[T], error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.enter("Load"); err != nil {
		return nil, err
	}

	e, ok := b.entries[id]
	if !ok {
		return nil, ErrNotFound
	}

	// The backing store's own expiry, which a real cache would apply for us.
	if !b.clock.Now().Before(e.expiresAt) {
		delete(b.entries, id)

		return nil, ErrNotFound
	}

	record := *e.record

	return &record, nil
}

func (b *fakeBackend[T]) Create(_ context.Context, id string, record *Record[T], ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.enter("Create"); err != nil {
		return err
	}

	if _, ok := b.entries[id]; ok {
		return ErrIDConflict
	}

	b.put(id, record, ttl)

	return nil
}

func (b *fakeBackend[T]) Update(_ context.Context, id string, record *Record[T], ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.enter("Update"); err != nil {
		return err
	}

	if _, ok := b.entries[id]; !ok {
		return ErrNotFound
	}

	b.put(id, record, ttl)

	return nil
}

func (b *fakeBackend[T]) Rename(_ context.Context, oldID, newID string, record *Record[T], ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.enter("Rename"); err != nil {
		return err
	}

	if _, ok := b.entries[oldID]; !ok {
		return ErrNotFound
	}
	if _, ok := b.entries[newID]; ok {
		return ErrIDConflict
	}

	delete(b.entries, oldID)
	b.put(newID, record, ttl)

	return nil
}

func (b *fakeBackend[T]) Delete(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.enter("Delete"); err != nil {
		return err
	}

	delete(b.entries, id)

	return nil
}

func (b *fakeBackend[T]) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true

	return nil
}

// put writes an entry. The caller holds the lock.
func (b *fakeBackend[T]) put(id string, record *Record[T], ttl time.Duration) {
	stored := *record
	b.entries[id] = entry[T]{record: &stored, expiresAt: b.clock.Now().Add(ttl)}
}

// ids returns every identifier currently stored.
func (b *fakeBackend[T]) ids() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return slices.Collect(maps.Keys(b.entries))
}

// newTestStore builds a store over a fresh fake backend and clock.
func newTestStore(tb testing.TB, opts ...Option) (Store[principal], *fakeBackend[principal], *fakeClock) {
	tb.Helper()

	c := newFakeClock()
	backend := newFakeBackend[principal](c)

	store, err := NewStore[principal](backend, append([]Option{WithClock(c)}, opts...)...)
	must.NoError(tb, err)

	return store, backend, c
}
