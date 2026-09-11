package operations

import (
	"errors"
	"testing"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The paths that need a real work queue — Start's enqueue and Recover's
// re-enqueue — are covered in containers_test.go, since a queue is a Postgres
// table and mocking one would only test the mock. What is covered here is
// everything a service does that a queue is not involved in.

func TestNewService(T *testing.T) {
	T.Parallel()

	T.Run("rejects what it cannot work without", func(t *testing.T) {
		t.Parallel()

		client := &fakeClient{}

		_, err := NewService(t.Context(), nil, client, newFakeStore(), nil, NewRegistry())
		test.ErrorIs(t, err, ErrNilConfig)

		// The client is what Start opens its own transaction on. Without one
		// there is no way in: Store has no WithTransaction of its own.
		_, err = NewService(t.Context(), &Config{}, nil, newFakeStore(), nil, NewRegistry())
		test.ErrorIs(t, err, ErrNilDatabaseClient)

		_, err = NewService(t.Context(), &Config{}, client, nil, nil, NewRegistry())
		test.ErrorIs(t, err, ErrNilStore)

		// The registry is required even on a process that runs nothing: Start
		// encodes through it, and refusing an unregistered kind is what keeps an
		// unrunnable operation out of the table.
		_, err = NewService(t.Context(), &Config{}, client, newFakeStore(), nil, nil)
		test.ErrorIs(t, err, ErrNilRegistry)

		// A service without a queue records operations that nothing ever runs,
		// which looks exactly like a working service until somebody waits for a
		// result.
		_, err = NewService(t.Context(), &Config{}, client, newFakeStore(), nil, NewRegistry())
		test.ErrorIs(t, err, ErrNilQueue)
	})
}

// newTestService builds a service over a fake store with no queue, for the reads
// and the cancellation, none of which touch one.
func newTestService(t *testing.T, store Store, registry *Registry) *StoreService {
	t.Helper()

	cfg := &Config{}
	cfg.EnsureDefaults()

	s := &StoreService{
		cfg:      *cfg,
		client:   &fakeClient{},
		store:    store,
		registry: registry,
		o11y:     observability.NewObserverForTest("operations_test"),
	}
	mp := metrics.EnsureMetricsProvider(nil)

	var err error

	s.startedCounter, err = mp.NewInt64Counter("started")
	must.NoError(t, err)

	s.cancelledCounter, err = mp.NewInt64Counter("cancelled")
	must.NoError(t, err)

	s.recoveredCounter, err = mp.NewInt64Counter("recovered")
	must.NoError(t, err)

	s.strandedCounter, err = mp.NewInt64Counter("stranded")
	must.NoError(t, err)

	return s
}

func TestService_Get(T *testing.T) {
	T.Parallel()

	store := newFakeStore(
		&Operation{ID: "op1", Kind: "export", State: StateRunning, Owner: testScope},
		&Operation{ID: "op2", Kind: "export", State: StateRunning, Owner: tenancy.Of("u2")},
	)
	svc := newTestService(T, store, NewRegistry())

	op, err := svc.Get(T.Context(), testScope, "op1")
	must.NoError(T, err)
	test.EqOp(T, "op1", op.ID)

	_, err = svc.Get(T.Context(), testScope, "nope")
	test.ErrorIs(T, err, ErrOperationNotFound)

	// Another tenant's operation reads as absent rather than as a refusal. The
	// two being one answer is what stops a status endpoint confirming which
	// guessed IDs are real.
	_, err = svc.Get(T.Context(), testScope, "op2")
	test.ErrorIs(T, err, ErrOperationNotFound)
}

func TestService_Cancel(T *testing.T) {
	T.Parallel()

	// Nothing has started, so there is nothing to ask and nobody to ask it of.
	T.Run("a pending operation is cancelled outright", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", Kind: "export", State: StatePending})
		svc := newTestService(t, store, NewRegistry())

		op, err := svc.Cancel(t.Context(), "op1")

		must.NoError(t, err)
		test.EqOp(t, StateCancelled, op.State)
		test.True(t, op.Done)
	})

	// A running operation keeps running until its Runner notices, because only
	// the Runner knows what a half-finished unit of its work has left behind.
	T.Run("a running operation is only flagged", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", Kind: "export", State: StateRunning})
		svc := newTestService(t, store, NewRegistry())

		op, err := svc.Cancel(t.Context(), "op1")

		must.NoError(t, err)
		test.EqOp(t, StateRunning, op.State)
		test.True(t, op.CancelRequested)
	})

	// The caller wanted it not running, and it is not running.
	T.Run("cancelling a finished operation is not an error", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "op1", Kind: "export", State: StateSucceeded, Done: true})
		svc := newTestService(t, store, NewRegistry())

		op, err := svc.Cancel(t.Context(), "op1")

		must.NoError(t, err)
		test.EqOp(t, StateSucceeded, op.State)
		test.False(t, op.CancelRequested)
	})

	T.Run("an unknown operation is reported", func(t *testing.T) {
		t.Parallel()

		svc := newTestService(t, newFakeStore(), NewRegistry())

		_, err := svc.Cancel(t.Context(), "nope")

		test.ErrorIs(t, err, ErrOperationNotFound)
	})
}

func TestService_Reap(T *testing.T) {
	T.Parallel()

	store := newFakeStore(
		&Operation{ID: "op1", State: StateSucceeded, Owner: testScope},
		&Operation{ID: "op2", State: StateFailed, Owner: testScope},
		&Operation{ID: "op3", State: StateRunning, Owner: testScope},
	)
	svc := newTestService(T, store, NewRegistry())

	reaped, err := svc.Reap(T.Context())

	must.NoError(T, err)
	test.EqOp(T, int64(2), reaped)

	// The running one is untouched: a reap that could delete an in-flight
	// operation would lose the only record that it is in flight.
	_, err = svc.Get(T.Context(), testScope, "op3")
	test.NoError(T, err)
}

func TestService_List(T *testing.T) {
	T.Parallel()

	store := newFakeStore(
		&Operation{ID: "op1", Owner: testScope, State: StateRunning},
		&Operation{ID: "op2", Owner: tenancy.Of("u2"), State: StateRunning},
	)
	svc := newTestService(T, store, NewRegistry())

	// The scope is List's own argument and there is no field on ListScope that
	// can widen past it, which is the whole reason the two are separate.
	results, err := svc.List(T.Context(), testScope, nil, nil)

	must.NoError(T, err)
	must.NotNil(T, results)
	must.SliceLen(T, 1, results.Data)
	test.EqOp(T, "op1", results.Data[0].ID)
}

func TestService_start_duplicate(T *testing.T) {
	T.Parallel()

	newRegistry := func(t *testing.T) *Registry {
		t.Helper()

		r := NewRegistry()
		must.NoError(t, Register(r, Definition[exportRequest]{
			Kind: "export",
			Run:  noopRun[exportRequest],
		}))

		return r
	}

	T.Run("returns the existing operation when the read succeeds", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(&Operation{ID: "fixed", Kind: "export", State: StateRunning, Owner: testScope})
		svc := newTestService(t, store, newRegistry(t))

		_, span := svc.o11y.Begin(t.Context())
		defer span.End()

		existing, err := svc.start(t.Context(), span, nil, "export", exportRequest{},
			[]StartOption{WithID("fixed"), WithOwner(testScope)})

		must.NoError(t, err)
		must.NotNil(t, existing)
		test.EqOp(t, "fixed", existing.ID)
	})

	T.Run("reports the read's own failure, not the duplicate sentinel", func(t *testing.T) {
		t.Parallel()

		// A transient database failure on the duplicate path used to come back as
		// ErrDuplicateOperation, so an operator debugging a database that blinked
		// was told the record already existed — and the caller, believing it,
		// stopped retrying.
		unreachable := platformerrors.New("the read replica is unreachable")

		store := newFakeStore(&Operation{ID: "fixed", Kind: "export", State: StateRunning, Owner: testScope})
		store.getErr = unreachable

		svc := newTestService(t, store, newRegistry(t))

		_, span := svc.o11y.Begin(t.Context())
		defer span.End()

		_, err := svc.start(t.Context(), span, nil, "export", exportRequest{},
			[]StartOption{WithID("fixed"), WithOwner(testScope)})

		must.Error(t, err)
		test.ErrorIs(t, err, unreachable)
		test.False(t, errors.Is(err, ErrDuplicateOperation))
	})
}
