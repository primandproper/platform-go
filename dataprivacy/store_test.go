package dataprivacy

import (
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runStoreSuite is the behavioral suite, run against every dialect.
//
// It is one function rather than a set of top-level tests so that SQLite and
// the container-backed servers cannot drift apart: a behavior asserted here is
// asserted everywhere, and a dialect-specific bug — MySQL's derived-table
// rewrite, Postgres's numbered placeholders, the partial indexes — shows up as
// a failure in the same named subtest rather than as a gap nobody noticed.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	suiteSaveAndGet(t, env)
	suiteTransition(t, env)
	suiteCompletion(t, env)
	suiteFail(t, env)
	suiteSweeps(t, env)
	suiteList(t, env)
}

func TestSQLStore_SQLite(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

func suiteSaveAndGet(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("round trips every field", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		completedAt := baseTime.Add(time.Hour)

		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		req.Status = StatusCompleted
		req.CompletedAt = &completedAt
		req.ExpiresAt = baseTime.Add(DefaultArtifactTTL)
		req.ArtifactRef = "dataprivacy/exports/x.json"
		req.ArtifactBytes = 4096
		req.Deleted = 7
		req.Anonymized = 3
		req.LastError = "something went wrong"
		req.Failures = map[string]string{"billing": "timed out"}
		req.Retained = map[string]string{"invoices": "tax law"}

		saveRequest(t, store, req)

		read, err := store.Get(t.Context(), req.ID)
		must.NoError(t, err)

		test.EqOp(t, req.ID, read.ID)
		test.EqOp(t, RequestExport, read.Type)
		test.EqOp(t, StatusCompleted, read.Status)
		test.EqOp(t, testSubject.ID, read.Subject.ID)
		test.EqOp(t, testSubject.Scope, read.Subject.Scope)
		test.EqOp(t, SubjectUser, read.Subject.Type)
		test.EqOp(t, req.ArtifactRef, read.ArtifactRef)
		test.EqOp(t, int64(4096), read.ArtifactBytes)
		test.EqOp(t, int64(7), read.Deleted)
		test.EqOp(t, int64(3), read.Anonymized)
		test.EqOp(t, req.OperationID, read.OperationID)
		test.EqOp(t, "something went wrong", read.LastError)
		test.Eq(t, req.Failures, read.Failures)
		test.Eq(t, req.Retained, read.Retained)
		test.True(t, read.RequestedAt.Equal(baseTime))
		test.True(t, read.ExpiresAt.Equal(req.ExpiresAt))
		must.NotNil(t, read.CompletedAt)
		test.True(t, read.CompletedAt.Equal(completedAt))
	})

	t.Run("a zero ExpiresAt round trips as zero", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// Bound as a value rather than NULL, a zero timestamp reads back as
		// year 1 — which every expiry sweep would treat as long overdue.
		req := saveRequest(t, store, newRequest(identifiers.New(), RequestExport, testSubject, baseTime))

		read, err := store.Get(t.Context(), req.ID)
		must.NoError(t, err)

		test.True(t, read.ExpiresAt.IsZero())
		test.Nil(t, read.CompletedAt)
	})

	t.Run("missing request reports ErrRequestNotFound", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.Get(t.Context(), "nope")
		test.True(t, errors.Is(err, ErrRequestNotFound))
	})

	t.Run("nil executor is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		err := store.Save(t.Context(), nil, newRequest("x", RequestExport, testSubject, baseTime))
		test.True(t, errors.Is(err, ErrNilExecutor))
	})
}

func suiteTransition(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("moves from an expected status", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		req := newRequest(identifiers.New(), RequestErasure, testSubject, baseTime)
		req.Status = StatusAwaitingConfirmation
		req.ExpiresAt = baseTime.Add(72 * time.Hour)
		saveRequest(t, store, req)

		var moved *Request

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			var err error
			moved, err = store.Transition(t.Context(), q, req.ID,
				[]Status{StatusAwaitingConfirmation}, StatusInProgress, "op-9", baseTime)

			return err
		}))

		test.EqOp(t, StatusInProgress, moved.Status)

		// The operation is recorded by the same statement, so the row cannot
		// become in progress without saying what is doing the work.
		test.EqOp(t, "op-9", moved.OperationID)

		// The confirmation window is cleared, or the lapse sweep would pick the
		// row back up and cancel a request that was just confirmed.
		test.True(t, moved.ExpiresAt.IsZero())
	})

	t.Run("refuses a status the guard excludes", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		req := saveRequest(t, store, newRequest(identifiers.New(), RequestErasure, testSubject, baseTime))

		err := store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			_, txErr := store.Transition(t.Context(), q, req.ID,
				[]Status{StatusAwaitingConfirmation}, StatusInProgress, "op-9", baseTime)

			return txErr
		})

		test.True(t, errors.Is(err, ErrRequestNotFound))
	})
}

func suiteCompletion(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("an export completion is guarded on the row being in progress", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		cancelled := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		cancelled.Status = StatusCancelled
		saveRequest(t, store, cancelled)
		cancelled.ArtifactRef = "x.json"
		cancelled.ExpiresAt = baseTime.Add(DefaultArtifactTTL)

		// A completion against a row that moved on would resurrect a request
		// somebody withdrew — which is exactly what a long export racing a
		// cancellation would otherwise do.
		err := store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.CompleteExport(t.Context(), q, cancelled, baseTime)
		})
		test.True(t, errors.Is(err, ErrRequestNotFound))

		req := saveRequest(t, store, newRequest(identifiers.New(), RequestExport, testSubject, baseTime))
		req.ArtifactRef = "x.json"
		req.ExpiresAt = baseTime.Add(DefaultArtifactTTL)

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.CompleteExport(t.Context(), q, req, baseTime)
		}))

		read, err := store.Get(t.Context(), req.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusCompleted, read.Status)
		test.EqOp(t, "x.json", read.ArtifactRef)
	})

	t.Run("an erasure completion records counts and retentions", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		req := saveRequest(t, store, newRequest(identifiers.New(), RequestErasure, testSubject, baseTime))

		req.Deleted = 12
		req.Anonymized = 4
		req.Retained = map[string]string{"billing.invoices": "tax law"}

		must.NoError(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.CompleteErasure(t.Context(), q, req, baseTime)
		}))

		read, err := store.Get(t.Context(), req.ID)
		must.NoError(t, err)
		test.EqOp(t, int64(12), read.Deleted)
		test.EqOp(t, int64(4), read.Anonymized)
		test.Eq(t, req.Retained, read.Retained)
		test.True(t, read.ExpiresAt.IsZero())
	})
}

func suiteFail(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a failure is terminal and stamps completion", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		req := saveRequest(t, store, newRequest(identifiers.New(), RequestExport, testSubject, baseTime))

		// There is no retryable branch any more. The retry schedule and the
		// attempt budget are the operation's, so the only failure this table
		// records is the last one.
		failed, err := store.Fail(t.Context(), req.ID, "fatal", baseTime)
		must.NoError(t, err)
		test.True(t, failed)

		read, err := store.Get(t.Context(), req.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusFailed, read.Status)
		test.EqOp(t, "fatal", read.LastError)
		must.NotNil(t, read.CompletedAt)
	})

	t.Run("a failure against a row that moved on writes nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		req.Status = StatusCancelled
		saveRequest(t, store, req)

		// Cancelled, or completed by a duplicate execution that got there
		// first: in both, the row already says something truer than "failed".
		failed, err := store.Fail(t.Context(), req.ID, "fatal", baseTime)
		must.NoError(t, err)
		test.False(t, failed)

		read, err := store.Get(t.Context(), req.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusCancelled, read.Status)
	})
}

func suiteSweeps(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("expiring artifacts selects only completed exports with a reference", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		due := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		due.Status = StatusCompleted
		due.ArtifactRef = "due.json"
		due.ExpiresAt = baseTime
		saveRequest(t, store, due)

		notYet := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		notYet.Status = StatusCompleted
		notYet.ArtifactRef = "later.json"
		notYet.ExpiresAt = baseTime.Add(time.Hour)
		saveRequest(t, store, notYet)

		alreadySwept := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		alreadySwept.Status = StatusExpired
		alreadySwept.ExpiresAt = baseTime
		saveRequest(t, store, alreadySwept)

		expiring, err := store.ExpiringArtifacts(t.Context(), baseTime, 10)
		must.NoError(t, err)
		must.SliceLen(t, 1, expiring)
		test.EqOp(t, due.ID, expiring[0].ID)
	})

	t.Run("marking expired clears the reference", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		req.Status = StatusCompleted
		req.ArtifactRef = "gone.json"
		req.ExpiresAt = baseTime
		saveRequest(t, store, req)

		must.NoError(t, store.MarkExpired(t.Context(), req.ID, baseTime))

		read, err := store.Get(t.Context(), req.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusExpired, read.Status)

		// A stale path must not outlive the object it named, or it could be
		// handed to a signer later.
		test.EqOp(t, "", read.ArtifactRef)
	})

	t.Run("lapsing cancels only unconfirmed erasures past their window", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		lapsed := newRequest(identifiers.New(), RequestErasure, testSubject, baseTime)
		lapsed.Status = StatusAwaitingConfirmation
		lapsed.ExpiresAt = baseTime.Add(-time.Minute)
		saveRequest(t, store, lapsed)

		live := newRequest(identifiers.New(), RequestErasure, testSubject, baseTime)
		live.Status = StatusAwaitingConfirmation
		live.ExpiresAt = baseTime.Add(time.Hour)
		saveRequest(t, store, live)

		count, err := store.LapseUnconfirmed(t.Context(), baseTime, 10)
		must.NoError(t, err)
		test.EqOp(t, int64(1), count)

		read, err := store.Get(t.Context(), lapsed.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusCancelled, read.Status)
		must.NotNil(t, read.CompletedAt)

		stillLive, err := store.Get(t.Context(), live.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusAwaitingConfirmation, stillLive.Status)
	})

	t.Run("overdue counts only unfulfilled requests", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		overdue := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		overdue.DueAt = baseTime.Add(-time.Hour)
		saveRequest(t, store, overdue)

		// Late, but served. A fact about the past is not a thing to page
		// somebody about.
		served := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		served.DueAt = baseTime.Add(-time.Hour)
		served.Status = StatusCompleted
		saveRequest(t, store, served)

		counts, err := store.CountOverdue(t.Context(), baseTime)
		must.NoError(t, err)
		test.EqOp(t, int64(1), counts[RequestExport])

		// Seeded to zero so a drained queue actively resets the gauge rather
		// than leaving a stale reading on the dashboard.
		test.EqOp(t, int64(0), counts[RequestErasure])
	})

	t.Run("reap spares a request whose artifact still exists", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		completedAt := baseTime.Add(-2 * DefaultRequestRetention)

		withArtifact := newRequest(identifiers.New(), RequestExport, testSubject, completedAt)
		withArtifact.Status = StatusCompleted
		withArtifact.ArtifactRef = "still-there.json"
		withArtifact.CompletedAt = &completedAt
		saveRequest(t, store, withArtifact)

		swept := newRequest(identifiers.New(), RequestExport, testSubject, completedAt)
		swept.Status = StatusExpired
		swept.CompletedAt = &completedAt
		saveRequest(t, store, swept)

		reaped, err := store.Reap(t.Context(), baseTime.Add(-DefaultRequestRetention), 10)
		must.NoError(t, err)
		test.EqOp(t, int64(1), reaped)

		// Deleting the row first would leave a file containing everything known
		// about a person with nothing left pointing at it.
		_, err = store.Get(t.Context(), withArtifact.ID)
		test.NoError(t, err)

		_, err = store.Get(t.Context(), swept.ID)
		test.True(t, errors.Is(err, ErrRequestNotFound))
	})
}

func suiteList(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("scopes to the subject and follows the filter's sort", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := saveRequest(t, store, newRequest(identifiers.New(), RequestExport, testSubject, baseTime))
		second := saveRequest(t, store, newRequest(identifiers.New(), RequestErasure, testSubject, baseTime))

		other := Subject{ID: "user-2", Type: SubjectUser, Scope: "account-1"}
		saveRequest(t, store, newRequest(identifiers.New(), RequestExport, other, baseTime))

		// filtering.DefaultQueryFilter asks for ascending, and this package
		// honors it rather than imposing a sort of its own.
		ascending, err := store.List(t.Context(), testSubject, filtering.DefaultQueryFilter())
		must.NoError(t, err)
		must.SliceLen(t, 2, ascending.Data)

		test.EqOp(t, first.ID, ascending.Data[0].ID)
		test.EqOp(t, second.ID, ascending.Data[1].ID)
		test.EqOp(t, uint64(2), ascending.TotalCount)

		filter := filtering.DefaultQueryFilter()
		filter.SortBy = filtering.SortDescending

		descending, err := store.List(t.Context(), testSubject, filter)
		must.NoError(t, err)
		must.SliceLen(t, 2, descending.Data)

		test.EqOp(t, second.ID, descending.Data[0].ID)
		test.EqOp(t, first.ID, descending.Data[1].ID)
	})

	t.Run("an empty scope matches every scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saveRequest(t, store, newRequest(identifiers.New(), RequestExport,
			Subject{ID: "user-1", Scope: "account-1"}, baseTime))
		saveRequest(t, store, newRequest(identifiers.New(), RequestExport,
			Subject{ID: "user-1", Scope: "account-2"}, baseTime))

		// A subject asking what has been requested in their name means all of
		// it; omitting the scoped requests would be the wrong answer.
		results, err := store.List(t.Context(), Subject{ID: "user-1"}, filtering.DefaultQueryFilter())
		must.NoError(t, err)
		test.SliceLen(t, 2, results.Data)
	})
}

// bogusDialectClient reports a dialect this package cannot emit SQL for.
//
// The unsupported-dialect branch is otherwise unreachable: the dialect comes
// from the client rather than the caller, and every client this module ships
// reports one of the three supported dialects. Only Dialect is consulted before
// the constructor gives up, so the embedded Client is never called.
type bogusDialectClient struct {
	database.Client
}

func (bogusDialectClient) Dialect() dialect.Dialect { return "oracle" }

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("rejects an invalid dialect", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(bogusDialectClient{newSQLiteEnv(t).client})
		test.True(t, errors.Is(err, dialect.ErrUnsupported))
	})

	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil)
		test.True(t, errors.Is(err, ErrNilDatabaseClient))
	})

	T.Run("rejects a prefix that is not an identifier", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(newSQLiteEnv(t).client, WithTablePrefix("drop table;--"))
		test.True(t, errors.Is(err, dialect.ErrInvalidIdentifier))
	})
}
