package dataprivacy

import (
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/jobs"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// sweeperEnv is a Sweeper wired over a live store and an in-memory bucket.
type sweeperEnv struct {
	store    Store
	sweeper  *Sweeper
	uploader *memoryUploader
	clock    *stubClock
}

func newSweeperEnv(t *testing.T, cfg *SweeperConfig, opts ...SweeperOption) *sweeperEnv {
	t.Helper()

	env := newSQLiteEnv(t)
	store := env.newStore(t)
	uploader := newMemoryUploader()
	stub := newStubClock()

	base := []SweeperOption{
		WithSweeperUploadManager(uploader),
		WithSweeperClock(stub),
	}

	sweeper, err := NewSweeper(t.Context(), cfg, store, append(base, opts...)...)
	must.NoError(t, err)

	return &sweeperEnv{store: store, sweeper: sweeper, uploader: uploader, clock: stub}
}

// completedExport saves a completed export whose artifact is in the bucket.
func (e *sweeperEnv) completedExport(t *testing.T, expiresAt time.Time) *Request {
	t.Helper()

	req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
	req.Status = StatusCompleted
	req.ArtifactRef = "dataprivacy/exports/" + req.ID + ".json"
	req.ExpiresAt = expiresAt
	saveRequest(t, e.store, req)

	must.NoError(t, uploadString(t, e.uploader, req.ArtifactRef, `{"data":{}}`))

	return req
}

func TestSweeper_ExpireArtifacts(T *testing.T) {
	T.Parallel()

	T.Run("deletes the object and clears the reference", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{})

		req := env.completedExport(t, baseTime.Add(-time.Minute))

		result, err := env.sweeper.Sweep(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(1), result.ArtifactsExpired)

		_, stillThere := env.uploader.get(req.ArtifactRef)
		test.False(t, stillThere)

		read, err := env.store.Get(t.Context(), req.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusExpired, read.Status)
		test.EqOp(t, "", read.ArtifactRef)
	})

	T.Run("leaves an artifact whose window is still open", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{})

		req := env.completedExport(t, baseTime.Add(time.Hour))

		result, err := env.sweeper.Sweep(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(0), result.ArtifactsExpired)

		_, stillThere := env.uploader.get(req.ArtifactRef)
		test.True(t, stillThere)
	})

	T.Run("a failed delete leaves the row alone for the next sweep", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{})

		req := env.completedExport(t, baseTime.Add(-time.Minute))

		env.uploader.mu.Lock()
		env.uploader.deleteErr = platformerrors.New("storage is unreachable")
		env.uploader.mu.Unlock()

		result, err := env.sweeper.Sweep(t.Context())
		must.NoError(t, err)

		// Marking it expired now would clear the only reference to an object
		// that still exists.
		test.EqOp(t, int64(0), result.ArtifactsExpired)

		read, err := env.store.Get(t.Context(), req.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusCompleted, read.Status)
		test.EqOp(t, req.ArtifactRef, read.ArtifactRef)

		// Once storage recovers, the next sweep finishes the job.
		env.uploader.mu.Lock()
		env.uploader.deleteErr = nil
		env.uploader.mu.Unlock()

		result, err = env.sweeper.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), result.ArtifactsExpired)
	})

	T.Run("an object already gone still expires the row", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{})

		req := env.completedExport(t, baseTime.Add(-time.Minute))

		// As if a previous sweep was interrupted between the delete and the row
		// update, or an operator cleaned up by hand.
		must.NoError(t, env.uploader.Delete(t.Context(), req.ArtifactRef))

		result, err := env.sweeper.Sweep(t.Context())
		must.NoError(t, err)

		// Retrying forever would leave the row pointing at nothing in
		// perpetuity, so the absence is confirmed and accepted.
		test.EqOp(t, int64(1), result.ArtifactsExpired)

		read, err := env.store.Get(t.Context(), req.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusExpired, read.Status)
	})
}

func TestSweeper_Lapse(T *testing.T) {
	T.Parallel()

	T.Run("cancels erasures nobody confirmed", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{})

		req := newRequest(identifiers.New(), RequestErasure, testSubject, baseTime)
		req.Status = StatusAwaitingConfirmation
		req.ExpiresAt = baseTime.Add(-time.Minute)
		saveRequest(t, env.store, req)

		result, err := env.sweeper.Sweep(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(1), result.ErasuresLapsed)

		read, err := env.store.Get(t.Context(), req.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusCancelled, read.Status)
	})
}

func TestSweeper_Reap(T *testing.T) {
	T.Parallel()

	T.Run("deletes terminal records past retention", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{RequestRetention: 24 * time.Hour})

		completedAt := baseTime.Add(-48 * time.Hour)

		req := newRequest(identifiers.New(), RequestExport, testSubject, completedAt)
		req.Status = StatusExpired
		req.CompletedAt = &completedAt
		saveRequest(t, env.store, req)

		result, err := env.sweeper.Sweep(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(1), result.RecordsReaped)
	})

	T.Run("DisableReap leaves records alone", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{RequestRetention: 24 * time.Hour, DisableReap: true})

		completedAt := baseTime.Add(-48 * time.Hour)

		req := newRequest(identifiers.New(), RequestExport, testSubject, completedAt)
		req.Status = StatusExpired
		req.CompletedAt = &completedAt
		saveRequest(t, env.store, req)

		result, err := env.sweeper.Sweep(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(0), result.RecordsReaped)

		_, err = env.store.Get(t.Context(), req.ID)
		test.NoError(t, err)
	})
}

func TestSweeper_Overdue(T *testing.T) {
	T.Parallel()

	T.Run("reports unfulfilled requests past their deadline", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{})

		overdue := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		overdue.DueAt = baseTime.Add(-time.Hour)
		saveRequest(t, env.store, overdue)

		result, err := env.sweeper.Sweep(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(1), result.Overdue[RequestExport])
		test.EqOp(t, int64(0), result.Overdue[RequestErasure])
	})

	T.Run("is reported even when nothing was swept", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{})

		// A sweep that found nothing to delete is exactly when nobody would
		// otherwise look at the number.
		result, err := env.sweeper.Sweep(t.Context())
		must.NoError(t, err)

		must.NotNil(t, result.Overdue)
		test.EqOp(t, int64(0), result.Overdue[RequestExport])
	})
}

func TestSweeper_Job(T *testing.T) {
	T.Parallel()

	T.Run("renders a registrable job", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{})

		job := env.sweeper.Job(jobs.MustCron("0 * * * *"), 30*time.Minute)

		test.EqOp(t, DefaultSweepJobName, job.Name)
		test.EqOp(t, 30*time.Minute, job.LeaseTTL)
		must.NotNil(t, job.Schedule)
		must.NotNil(t, job.Run)

		test.NoError(t, job.Run(t.Context()))
	})
}

func TestNewSweeper(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil store", func(t *testing.T) {
		t.Parallel()

		_, err := NewSweeper(t.Context(), &SweeperConfig{}, nil)
		test.ErrorIs(t, err, ErrNilStore)
	})

	T.Run("without storage it expires nothing", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		sweeper, err := NewSweeper(t.Context(), &SweeperConfig{}, store)
		must.NoError(t, err)

		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		req.Status = StatusCompleted
		req.ArtifactRef = "orphan.json"
		req.ExpiresAt = baseTime.Add(-time.Hour)
		saveRequest(t, store, req)

		result, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		// A row that says the artifact is gone while the artifact is not is
		// worse than no sweep, because it stops anybody looking.
		test.EqOp(t, int64(0), result.ArtifactsExpired)

		read, err := store.Get(t.Context(), req.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusCompleted, read.Status)
	})
}

// uploadString writes a string object into the uploader.
func uploadString(t *testing.T, uploader *memoryUploader, path, content string) error {
	t.Helper()

	return uploader.Save(t.Context(), path, stringReader(content))
}
