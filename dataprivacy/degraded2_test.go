package dataprivacy

import (
	"context"
	"database/sql"
	"io"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/uploads"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// uncountableResult is a sql.Result that cannot say how many rows it touched.
//
// Drivers are permitted to refuse this, and every guarded write in the store
// reads it to decide whether the row it meant to move actually moved. Treating
// an unreadable count as success would report an export completed against a row
// that says otherwise.
type unountableResult struct{}

var _ sql.Result = (*unountableResult)(nil)

func (*unountableResult) LastInsertId() (int64, error) { return 0, errDatabase }
func (*unountableResult) RowsAffected() (int64, error) { return 0, errDatabase }

// uncountableExecutor executes fine but returns results whose counts fail.
type uncountableExecutor struct {
	database.SQLQueryExecutor
}

func (*uncountableExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return &unountableResult{}, nil
}

// uncountableClient hands out uncountable executors.
type uncountableClient struct {
	database.Client
}

func (c *uncountableClient) Writer() database.SQLQueryExecutor {
	return &uncountableExecutor{SQLQueryExecutor: c.Client.Writer()}
}

func (c *uncountableClient) WithTransaction(ctx context.Context, fn func(database.SQLQueryExecutor) error) error {
	return c.Client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		return fn(&uncountableExecutor{SQLQueryExecutor: q})
	})
}

func newUncountableStore(t *testing.T) Store {
	t.Helper()

	env := newSQLiteEnv(t)

	// Migrate through the real client, then wrap it.
	_ = env.newStore(t)

	store, err := NewSQLStore(&uncountableClient{Client: env.client})
	must.NoError(t, err)

	return store
}

func TestSQLStore_UnreadableRowCounts(T *testing.T) {
	T.Parallel()

	T.Run("are reported rather than treated as success", func(t *testing.T) {
		t.Parallel()

		store := newUncountableStore(t)

		err := store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			_, txErr := store.Transition(t.Context(), q, "r", []Status{StatusInProgress}, StatusCancelled, "", baseTime)

			return txErr
		})
		test.ErrorIs(t, err, errDatabase)

		err = store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return store.CompleteExport(t.Context(), q, newRequest("r", RequestExport, testSubject, baseTime), baseTime)
		})
		test.ErrorIs(t, err, errDatabase)

		_, err = store.LapseUnconfirmed(t.Context(), baseTime, 10)
		test.ErrorIs(t, err, errDatabase)

		_, err = store.Reap(t.Context(), baseTime, 10)
		test.ErrorIs(t, err, errDatabase)
	})
}

func TestSQLStore_CorruptStoredMaps(T *testing.T) {
	T.Parallel()

	T.Run("a failures column that is not JSON is reported", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		req := saveRequest(t, store, newRequest(identifiers.New(), RequestExport, testSubject, baseTime))

		// Whatever wrote this was not this package, but the read still has to
		// say so rather than hand back a half-decoded request.
		prefix := storePrefix(t, store)

		_, err := env.client.Writer().ExecContext(t.Context(),
			"UPDATE "+prefix+"_dataprivacy_requests SET failures = ? WHERE id = ?", []byte("{not json"), req.ID)
		must.NoError(t, err)

		_, err = store.Get(t.Context(), req.ID)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "decoding dataprivacy request failures")
	})
}

// storePrefix digs the rendered table prefix out of a SQL store, so a test can
// reach past the interface to corrupt a column.
func storePrefix(t *testing.T, store Store) string {
	t.Helper()

	s, ok := store.(*SQLStore)
	must.True(t, ok)

	return s.tables.prefix()
}

// failingSaveUploader accepts nothing.
type failingSaveUploader struct {
	*memoryUploader
}

func (*failingSaveUploader) Save(context.Context, string, io.Reader, ...uploads.SaveOption) error {
	return platformerrors.New("the bucket is read-only")
}

// unsignableUploader can sign in principle but always refuses.
type unsignableUploader struct {
	*memoryUploader
}

func (*unsignableUploader) SignedURL(context.Context, string, *uploads.SignedURLOptions) (string, error) {
	return "", platformerrors.New("signing key is unavailable")
}

func TestFulfiller_DegradedStorage(T *testing.T) {
	T.Parallel()

	T.Run("an unwritable bucket fails the export", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		})

		env.fulfiller.uploader = &failingSaveUploader{memoryUploader: env.uploader}

		req := env.submitAndRun(t, RequestExport)

		// Retryable — the bucket may come back — so the row only says so on the
		// final attempt, which is the one submitAndRun runs.
		test.EqOp(t, StatusFailed, req.Status)
		test.StrContains(t, req.LastError, "storing dataprivacy export artifact")
	})

	T.Run("an unknown request type fails terminally", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		})

		// A row written by a newer build than this one, reached by the export
		// runner because that is the kind its operation names.
		req := newRequest(identifiers.New(), RequestType("rectification"), testSubject, env.clock.read())
		saveRequest(t, env.store, req)

		_, err := env.run(t, req.ID, RequestExport, newRecordingReporter(
			operations.Attempt{ID: "op-1", Number: 1}))

		// Unretryable: the row and the kind disagree, and no retry resolves it.
		must.Error(t, err)
		test.True(t, operations.IsUnretryable(err))
		test.StrContains(t, err.Error(), "started as a")
	})

	T.Run("a signer that refuses yields a notification with no link", func(t *testing.T) {
		t.Parallel()

		uploader := &unsignableUploader{memoryUploader: newMemoryUploader()}

		sign := NewArtifactURLSigner(uploader, time.Minute, false)

		url, expiresAt := sign(t.Context(), &Request{ArtifactRef: "exports/x.json"})

		// Better an email saying "sign in to download" than one carrying a link
		// that does not work.
		test.EqOp(t, "", url)
		test.True(t, expiresAt.IsZero())
	})

	T.Run("an operation naming no request fails terminally", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		})

		// An empty Job decodes from an absent request payload, which is what an
		// operation started without one leaves behind.
		_, err := env.run(t, "", RequestExport, newRecordingReporter(
			operations.Attempt{ID: "op-1", Number: 1}))

		must.Error(t, err)
		test.True(t, operations.IsUnretryable(err))
	})
}

func TestService_DegradedDependencies(T *testing.T) {
	T.Parallel()

	// Note there is no "invalid config is refused" case here, deliberately.
	// ServiceConfig.EnsureDefaults replaces every non-positive field with its
	// default, so ValidateWithContext cannot currently fail through NewService.
	// The call is kept as insurance for a future field that defaults cannot
	// rescue — FulfillerConfig already has one, in the timeout cross-check —
	// but contriving a test to reach it today would assert nothing real.

	T.Run("a failing store surfaces through every read and write", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), &ServiceConfig{}, newFailingStore(t), newStubOperations(),
			WithServiceClock(newStubClock()),
			WithServiceUploadManager(newMemoryUploader()),
		)
		must.NoError(t, err)

		_, err = svc.Submit(t.Context(), testSubject, RequestExport)
		test.ErrorIs(t, err, errDatabase)

		_, err = svc.List(t.Context(), testSubject, filtering.DefaultQueryFilter())
		test.ErrorIs(t, err, errDatabase)

		// Confirm maps a genuine store failure to the failure, not to
		// "not awaiting confirmation" — the two mean very different things to
		// somebody debugging a stuck request.
		_, err = svc.Confirm(t.Context(), "r")
		test.Error(t, err)
	})

	T.Run("without an upload manager there is no artifact", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		svc, err := NewService(t.Context(), &ServiceConfig{}, store, newStubOperations(), WithServiceClock(newStubClock()))
		must.NoError(t, err)

		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		req.Status = StatusCompleted
		req.ArtifactRef = "exports/x.json"
		saveRequest(t, store, req)

		_, err = svc.Download(t.Context(), req.ID)
		test.ErrorIs(t, err, ErrArtifactUnavailable)

		_, err = svc.Open(t.Context(), req.ID)
		test.ErrorIs(t, err, ErrArtifactUnavailable)
	})

	T.Run("a missing object is reported rather than returned empty", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		svc, err := NewService(t.Context(), &ServiceConfig{}, store, newStubOperations(),
			WithServiceClock(newStubClock()),
			WithServiceUploadManager(newMemoryUploader()),
		)
		must.NoError(t, err)

		// The row says there is an artifact; the bucket disagrees.
		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		req.Status = StatusCompleted
		req.ArtifactRef = "exports/missing.json"
		saveRequest(t, store, req)

		_, err = svc.Open(t.Context(), req.ID)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "reading dataprivacy artifact")
	})
}
