package dataprivacy

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The store is the layer the database client cannot speak for. otelsql traces
// the statement, but with the SQL suppressed by default and no idea which
// request it was about — and, more to the point, a guarded UPDATE that matches
// no row is a perfectly successful statement down there. Every assertion in
// this file is about something invisible from either side: the store's own
// spans, and the outcomes that are not errors.

// recordingStore returns a store whose observations are captured, alongside the
// recorder. Swapped in after construction because NewSQLStore builds the real
// Observer from the options; the seam is the field, not a constructor argument.
func recordingStore(t *testing.T, env *storeEnv) (Store, *observability.RecordingObserver) {
	t.Helper()

	store := env.newStore(t)
	recorder := observability.NewRecordingObserver()

	concrete, ok := store.(*SQLStore)
	must.True(t, ok)
	concrete.o11y = recorder

	return store, recorder
}

func TestSQLStore_Observability(T *testing.T) {
	T.Parallel()

	T.Run("a save names the request, its type, and its subject", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, recorder := recordingStore(t, env)

		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		saveRequest(t, store, req)

		recorder.ObservedOperationWithData(t, map[string]any{
			requestIDKey:   req.ID,
			requestTypeKey: string(RequestExport),
			statusKey:      string(StatusInProgress),
			subjectIDKey:   testSubject.ID,
		})
	})

	T.Run("a missing request is marked on the span but not raised as an error", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, recorder := recordingStore(t, env)

		_, err := store.Get(t.Context(), "nope")
		test.ErrorIs(t, err, ErrRequestNotFound)

		// A request ID that is not in the table is a 404 somebody is owed, or a
		// record retention has swept. Routing it through op.Error would log at
		// error level and mark the span failed, and a trace that is red for every
		// polling client is a trace nobody reads.
		op := recorder.ObservedOperationWithData(t, map[string]any{guardMissedKey: true})
		test.SliceEmpty(t, op.Errors)
	})

	T.Run("a transition that moves nothing records why", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, recorder := recordingStore(t, env)

		req := newRequest(identifiers.New(), RequestErasure, testSubject, baseTime)
		req.Status = StatusInProgress
		saveRequest(t, store, req)

		// Guarded on a status the request is not in — the shape of a subject
		// confirming an erasure twice, or confirming one the sweep just lapsed.
		err := store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			_, transitionErr := store.Transition(
				t.Context(), q, req.ID, []Status{StatusAwaitingConfirmation}, StatusInProgress, "op-1", baseTime,
			)

			return transitionErr
		})
		test.ErrorIs(t, err, ErrRequestNotFound)

		// The guard that matched nothing is the whole story, so the statuses it
		// guarded on are on the span beside the miss.
		recorder.ObservedOperationWithData(t, map[string]any{
			requestIDKey:    req.ID,
			fromStatusKey:   string(StatusAwaitingConfirmation),
			statusKey:       string(StatusInProgress),
			operationIDKey:  "op-1",
			rowsAffectedKey: int64(0),
			guardMissedKey:  true,
		})
	})

	T.Run("a transition that moves a row records the count, not a miss", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, recorder := recordingStore(t, env)

		req := newRequest(identifiers.New(), RequestErasure, testSubject, baseTime)
		req.Status = StatusAwaitingConfirmation
		saveRequest(t, store, req)

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			_, transitionErr := store.Transition(
				t.Context(), q, req.ID, []Status{StatusAwaitingConfirmation}, StatusInProgress, "op-1", baseTime,
			)

			return transitionErr
		}))

		op := recorder.ObservedOperationWithData(t, map[string]any{rowsAffectedKey: int64(1)})
		test.SliceEmpty(t, op.Errors)

		for _, observation := range op.Observations {
			test.StrNotEqFold(t, guardMissedKey, observation.Key)
		}
	})

	T.Run("a completion that lost its row says so", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, recorder := recordingStore(t, env)

		// Completed rather than processing: the shape of a long export whose
		// request was cancelled or expired while the worker was busy. The work is
		// done and there is nowhere to record it.
		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		req.Status = StatusCancelled
		saveRequest(t, store, req)

		req.ArtifactRef = "dataprivacy/exports/x.json"
		req.ArtifactBytes = 2048

		err := store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.CompleteExport(t.Context(), q, req, baseTime)
		})
		test.ErrorIs(t, err, ErrRequestNotFound)

		recorder.ObservedOperationWithData(t, map[string]any{
			requestIDKey:    req.ID,
			artifactRefKey:  req.ArtifactRef,
			artifactSizeKey: req.ArtifactBytes,
			rowsAffectedKey: int64(0),
			guardMissedKey:  true,
		})
	})

	T.Run("an erasure completion records what it destroyed and kept", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, recorder := recordingStore(t, env)

		req := newRequest(identifiers.New(), RequestErasure, testSubject, baseTime)
		req.Status = StatusInProgress
		saveRequest(t, store, req)

		req.Deleted = 7
		req.Anonymized = 3
		req.Retained = map[string]string{"invoices": "financial records"}

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.CompleteErasure(t.Context(), q, req, baseTime)
		}))

		// The counts a regulator asks about, on the span that recorded them.
		recorder.ObservedOperationWithData(t, map[string]any{
			deletedKey:    int64(7),
			anonymizedKey: int64(3),
			retainedKey:   1,
		})
	})

	T.Run("a failure that matched no row records the miss", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, recorder := recordingStore(t, env)

		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		req.Status = StatusCancelled
		saveRequest(t, store, req)

		// The row left StatusInProgress before the final attempt gave up:
		// cancelled, or completed by a duplicate execution that got there first.
		// Recorded rather than returned, because in both of those the row
		// already says something truer than "failed" would.
		failed, err := store.Fail(t.Context(), req.ID, "boom", baseTime)
		must.NoError(t, err)
		test.False(t, failed)

		recorder.ObservedOperationWithData(t, map[string]any{
			requestIDKey:    req.ID,
			rowsAffectedKey: int64(0),
			guardMissedKey:  true,
		})
	})

	T.Run("a listing records the page size and the total", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, recorder := recordingStore(t, env)

		for range 2 {
			saveRequest(t, store, newRequest(identifiers.New(), RequestExport, testSubject, baseTime))
		}

		_, err := store.List(t.Context(), testSubject, nil)
		must.NoError(t, err)

		recorder.ObservedOperationWithData(t, map[string]any{
			subjectIDKey:   testSubject.ID,
			resultCountKey: 2,
			resultTotalKey: uint64(2),
		})
	})

	T.Run("the sweeps record what they moved", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, recorder := recordingStore(t, env)

		lapsing := newRequest(identifiers.New(), RequestErasure, testSubject, baseTime)
		lapsing.Status = StatusAwaitingConfirmation
		lapsing.ExpiresAt = baseTime
		saveRequest(t, store, lapsing)

		lapsed, err := store.LapseUnconfirmed(t.Context(), baseTime.Add(time.Hour), 10)
		must.NoError(t, err)
		test.EqOp(t, int64(1), lapsed)

		recorder.ObservedOperationWithData(t, map[string]any{lapsedKey: int64(1)})

		reaped, err := store.Reap(t.Context(), baseTime.Add(365*24*time.Hour), 10)
		must.NoError(t, err)

		recorder.ObservedOperationWithData(t, map[string]any{reapedKey: reaped})
	})

	T.Run("a nil executor is a fault, and reported as one", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, recorder := recordingStore(t, env)

		// Unlike a guard miss, this one is a programming error: nothing downstream
		// can recover from it and it should be loud.
		test.ErrorIs(t, store.CompleteExport(t.Context(), nil, nil, baseTime), ErrNilExecutor)

		op := recorder.ObservedOperationWithKeys(t)
		test.SliceNotEmpty(t, op.Errors)
	})

	T.Run("every operation ends its span", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, recorder := recordingStore(t, env)

		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		saveRequest(t, store, req)

		_, err := store.Get(t.Context(), req.ID)
		must.NoError(t, err)

		_, err = store.CountOverdue(t.Context(), baseTime.Add(365*24*time.Hour))
		must.NoError(t, err)

		// A span left open is a leak the exporter never reports and no test
		// notices, which is why every method defers End rather than calling it on
		// the success path.
		must.SliceNotEmpty(t, recorder.Operations)

		for _, op := range recorder.Operations {
			test.True(t, op.Ended)
		}
	})
}
