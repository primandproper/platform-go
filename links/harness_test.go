package links

import (
	"context"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/clock"
	clockmock "github.com/primandproper/primitives-go/clock/mock"
	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test/must"
)

const (
	testAction    Action  = "magic_login"
	testSubject   Subject = "user_123"
	testActionTTL         = 15 * time.Minute
)

// testPolicy is the action every test mints under unless it needs otherwise.
func testPolicy() ActionPolicy {
	return ActionPolicy{
		URL: "https://app.example.com/auth/magic/{token}",
		TTL: testActionTTL,
	}
}

// errDuplicateRecord is what the store double reports for an id already in the
// map, standing in for the primary key or the SETNX the real ones lean on.
var errDuplicateRecord = platformerrors.New("action link record already stored")

// memoryStore is a links.Store over a map, and the double every test in this
// package runs against.
//
// It is hand-written rather than generated, and it is a real implementation
// rather than a set of canned answers, because what a Minter is being tested
// against here is the Store contract: absent reads as ErrLinkNotFound, a record
// from another shape reads as ErrStaleRecord, and Resolve is one atomic step
// that either transitions the link or says why it would not. A mock returning
// whatever a test asked for would let the Minter drift from all three.
//
// The shipped store is tested against its own storage — links/database against
// a real engine — so what that suite covers and this one cannot is the
// atomicity: here the mutex supplies it, which is exactly what a table has to
// buy from a guarded UPDATE.
//
// It cannot live in links/mock: an in-package test importing that would close
// an import cycle.
type memoryStore struct {
	records map[ID]*Record

	getErr     error
	putErr     error
	resolveErr error
	revokeErr  error

	mu sync.Mutex
}

var _ Store = (*memoryStore)(nil)

// newMemoryStore builds an empty store.
func newMemoryStore() *memoryStore {
	return &memoryStore{records: map[ID]*Record{}}
}

// Put writes a record, refusing an id already in the map for the reason the
// real stores do: a collision means the generator repeated itself.
func (s *memoryStore) Put(_ context.Context, id ID, record *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.putErr != nil {
		return s.putErr
	}

	if _, ok := s.records[id]; ok {
		return errDuplicateRecord
	}

	stored := *record
	stored.Metadata = maps.Clone(record.Metadata)
	s.records[id] = &stored

	return nil
}

// Get reads a record without consuming it.
func (s *memoryStore) Get(_ context.Context, id ID) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.read(id)
}

// Resolve transitions a link under the mutex, which is what the database store
// buys with a guarded UPDATE.
func (s *memoryStore) Resolve(
	_ context.Context,
	id ID,
	to State,
	at, purgeAfter time.Time,
) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.resolveErr != nil {
		return nil, s.resolveErr
	}

	record, err := s.read(id)
	if err != nil {
		return nil, err
	}

	if err = record.Usable(at); err != nil {
		return record, err
	}

	resolved := *record
	resolved.State = to
	resolved.ResolvedAt = &at
	resolved.PurgeAfter = purgeAfter

	s.records[id] = &resolved

	return &resolved, nil
}

// RevokeForSubject moves every unresolved record for a subject, which is the
// double's version of the one UPDATE links/database issues.
//
// It refuses on a deadline no more than that statement does: a record that
// expired without being resolved is still moved, and the count still includes
// it.
func (s *memoryStore) RevokeForSubject(
	_ context.Context,
	subject Subject,
	at, purgeAfter time.Time,
) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.revokeErr != nil {
		return 0, s.revokeErr
	}

	var revoked int64

	for id, record := range s.records {
		if record.Subject != subject || record.ResolvedAt != nil {
			continue
		}

		moved := *record
		moved.State = StateRevoked
		moved.ResolvedAt = &at
		moved.PurgeAfter = purgeAfter

		s.records[id] = &moved
		revoked++
	}

	return revoked, nil
}

// read is the shared body of Get and Resolve's read half, held under the mutex
// by both.
func (s *memoryStore) read(id ID) (*Record, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}

	record, ok := s.records[id]
	if !ok {
		return nil, ErrLinkNotFound
	}

	if !record.Current() {
		return nil, ErrStaleRecord
	}

	// The live pointer, as the memory cache provider hands back, so a test can
	// tell whether the Minter copies what it returns to a caller.
	return record, nil
}

// stored reads a record out from under the store, for the assertions about what
// was written rather than about what was answered.
func (s *memoryStore) stored(tb testing.TB, id ID) *Record {
	tb.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[id]
	must.True(tb, ok)

	return record
}

// newTestMinter builds a Minter over a memory store, with the default action
// registered.
func newTestMinter(tb testing.TB, opts ...Option) *Minter {
	tb.Helper()

	m, _ := newTestMinterStore(tb, opts...)

	return m
}

// newTestMinterStore builds a Minter and hands back the store behind it, for
// the tests that assert on what was written.
func newTestMinterStore(tb testing.TB, opts ...Option) (*Minter, *memoryStore) {
	tb.Helper()

	store := newMemoryStore()

	m, err := NewMinter(store, append([]Option{
		WithAction(testAction, testPolicy()),
	}, opts...)...)
	must.NoError(tb, err)

	return m, store
}

// testClock is a clock whose time only moves when a test moves it, so expiry
// can be reached without waiting for it.
type testClock struct {
	now time.Time
	mu  sync.Mutex
}

// newTestClock starts a clock at a fixed instant.
func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)}
}

// Advance moves the clock forward.
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// Clock adapts the test clock to the interface the Minter takes. Only Now and
// Since are reachable from this package, so the rest is left to panic rather
// than given a plausible-looking implementation nothing exercises.
func (c *testClock) Clock() clock.Clock {
	return &clockmock.ClockMock{
		NowFunc: func() time.Time {
			c.mu.Lock()
			defer c.mu.Unlock()

			return c.now
		},
		SinceFunc: func(t time.Time) time.Duration {
			c.mu.Lock()
			defer c.mu.Unlock()

			return c.now.Sub(t)
		},
	}
}
