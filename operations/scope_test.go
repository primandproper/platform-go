package operations

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The scope is the argument the reads exist to bind, so what is worth pinning is
// the three ways it can be got wrong: an entity that disagrees with it, a read
// that reaches the store without an executor to bind it through, and a watch
// loop that re-reads a subscription under somebody else's.

func TestAdoptScope(T *testing.T) {
	T.Parallel()

	T.Run("an operation that names none adopts the argument", func(t *testing.T) {
		t.Parallel()

		op := &Operation{ID: "op1"}

		must.NoError(t, adoptScope(testScope, op))
		test.EqOp(t, testScope, op.Owner)
	})

	// The distinction a string could not have made. Global() is a scope somebody
	// chose, so it is left alone rather than overwritten by the argument — and
	// the zero Scope above is genuinely "nobody said" rather than the global
	// scope spelled shortly.
	T.Run("an operation that names the global scope keeps it", func(t *testing.T) {
		t.Parallel()

		op := &Operation{ID: "op1", Owner: tenancy.Global()}

		must.NoError(t, adoptScope(tenancy.Global(), op))
		test.EqOp(t, tenancy.Global(), op.Owner)

		mismatched := &Operation{ID: "op1", Owner: tenancy.Global()}
		test.ErrorIs(t, adoptScope(testScope, mismatched), ErrScopeMismatch)
	})

	// Refused rather than corrected: the two disagreeing is a caller holding one
	// tenant's operation and recording it under another, which is a stale value
	// or a mix-up and not a thing to guess at.
	T.Run("an operation that names another scope is refused", func(t *testing.T) {
		t.Parallel()

		op := &Operation{ID: "op1", Owner: tenancy.Of("u2")}

		err := adoptScope(testScope, op)

		must.ErrorIs(t, err, ErrScopeMismatch)

		// Unchanged, so a caller that recovers from this reads the value it
		// actually held rather than the one this call would have written.
		test.EqOp(t, tenancy.Of("u2"), op.Owner)
	})
}

// TestSQLStore_ReadsRequireAnExecutor. Every read here binds the scope through
// the executor the caller supplies, so there is no read that can fall back to a
// connection of the store's own — which is what makes a caller inside a
// transaction able to see their own uncommitted writes.
func TestSQLStore_ReadsRequireAnExecutor(T *testing.T) {
	T.Parallel()

	store, err := NewSQLStore(storeClient(dialect.Postgres))
	must.NoError(T, err)

	_, err = store.Get(T.Context(), nil, testScope, "op1")
	test.ErrorIs(T, err, ErrNilExecutor)

	_, err = store.GetMany(T.Context(), nil, testScope, []string{"op1"})
	test.ErrorIs(T, err, ErrNilExecutor)

	_, err = store.List(T.Context(), nil, testScope, nil, nil)
	test.ErrorIs(T, err, ErrNilExecutor)

	_, err = store.Insert(T.Context(), nil, testScope, &Operation{ID: "op1"})
	test.ErrorIs(T, err, ErrNilExecutor)
}

// TestWatcher_ReadsUnderTheScopeItSubscribedWith. A subscription is a standing
// permission to see one row, granted by a scoped read at Watch and held for
// every re-read after it — rather than by a comparison the sweep would have to
// remember to make.
func TestWatcher_ReadsUnderTheScopeItSubscribedWith(T *testing.T) {
	T.Parallel()

	T.Run("another tenant's operation cannot be subscribed to", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", Owner: testScope, State: StateRunning})
		w := newTestWatcher(t, store, nil)

		_, err := w.Watch(t.Context(), tenancy.Of("u2"), "op1")

		// Absent rather than refused, so a subscription endpoint is not an
		// oracle for which guessed IDs are real.
		test.ErrorIs(t, err, ErrOperationNotFound)
	})

	T.Run("the sweep re-reads one scope per statement", func(t *testing.T) {
		t.Parallel()

		other := tenancy.Of("u2")

		store := newFakeStore(
			&Operation{ID: "op1", Owner: testScope, State: StateRunning, Revision: 1},
			&Operation{ID: "op2", Owner: other, State: StateRunning, Revision: 1},
		)
		w := newTestWatcher(t, store, nil)

		mine, err := w.Watch(t.Context(), testScope, "op1")
		must.NoError(t, err)

		theirs, err := w.Watch(t.Context(), other, "op2")
		must.NoError(t, err)

		_, _ = receive(t, mine)
		_, _ = receive(t, theirs)

		// Two scopes, one id each: two statements, and neither of them can see
		// the other's row.
		watched := w.watchedByScope()
		must.MapLen(t, 2, watched)
		test.Eq(t, []string{"op1"}, watched[testScope])
		test.Eq(t, []string{"op2"}, watched[other])

		// Both still reach their subscriber, which is the half a scoped batch
		// read could plausibly have broken.
		for id, snapshots := range map[string]<-chan *Operation{"op1": mine, "op2": theirs} {
			finished := *store.snapshot(id)
			finished.State = StateSucceeded
			store.put(&finished)

			w.sweep(t.Context())

			delivered, ok := receive(t, snapshots)
			must.True(t, ok, must.Sprintf("operation %q", id))
			test.EqOp(t, StateSucceeded, delivered.State, test.Sprintf("operation %q", id))
		}
	})
}
