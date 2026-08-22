package dataprivacy

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/shredding"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/operations"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingShredder stands in for the key store, so these tests are about when
// dataprivacy shreds rather than about how shredding works.
type recordingShredder struct {
	err      error
	subjects []shredding.Subject
	at       atomic.Int64
}

var _ shredding.Shredder = (*recordingShredder)(nil)

// unscopedSubject is the shape a "forget me entirely" request carries. The
// suite's testSubject is confined to one account, which is precisely the case a
// shred cannot serve — see TestFulfiller_ShredScoped.
var unscopedSubject = Subject{ID: "user-1", Type: SubjectUser}

// runErasureFor saves an erasure for the subject and runs one attempt at it.
func runErasureFor(t *testing.T, env *fulfillerEnv, subject Subject) *Request {
	t.Helper()

	req := saveRequest(t, env.store,
		newRequest(identifiers.New(), RequestErasure, subject, env.clock.read()))

	// The error is deliberately ignored: what these tests assert is the row the
	// attempt left behind, and half of them are about attempts that fail.
	_, _ = env.run(t, req.ID, RequestErasure, newRecordingReporter(
		operations.Attempt{ID: "op-1", Number: 1}))

	return env.reread(t, req.ID)
}

func (s *recordingShredder) Shred(_ context.Context, subject shredding.Subject) (shredding.Receipt, error) {
	if s.err != nil {
		return shredding.Receipt{}, s.err
	}

	s.subjects = append(s.subjects, subject)
	s.at.Add(1)

	return shredding.Receipt{Subject: subject, ShreddedAt: baseTime, Destroyed: true}, nil
}

func TestFulfiller_Shred(T *testing.T) {
	T.Parallel()

	T.Run("destroys the subject's key and records when", func(t *testing.T) {
		t.Parallel()

		var ran atomic.Int64

		shredder := &recordingShredder{}
		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(3, 0, nil, &ran)))
		}, WithFulfillerShredder(shredder))

		req := runErasureFor(t, env, unscopedSubject)

		test.EqOp(t, StatusCompleted, req.Status)
		must.NotNil(t, req.KeyShreddedAt)
		test.EqOp(t, baseTime, req.KeyShreddedAt.UTC())

		must.SliceLen(t, 1, shredder.subjects)
		test.EqOp(t, shredding.Subject{Type: string(SubjectUser), ID: testSubject.ID}, shredder.subjects[0])

		// The erasers still run. Shredding covers what the application chose to
		// encrypt per subject; the rows are still the erasers' job.
		test.EqOp(t, int64(1), ran.Load())
	})

	T.Run("does nothing to an export", func(t *testing.T) {
		t.Parallel()

		shredder := &recordingShredder{}
		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"a":1}`)))
		}, WithFulfillerShredder(shredder))

		req := env.submitAndRun(t, RequestExport)

		test.EqOp(t, StatusCompleted, req.Status)
		test.Nil(t, req.KeyShreddedAt)
		test.SliceEmpty(t, shredder.subjects)
	})

	T.Run("shreds before the erasers run", func(t *testing.T) {
		t.Parallel()

		var shreddedFirst atomic.Bool

		shredder := &recordingShredder{}
		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity",
				EraserFunc(func(context.Context, database.SQLQueryExecutor, Subject) (ErasureOutcome, error) {
					shreddedFirst.Store(shredder.at.Load() == 1)

					return ErasureOutcome{Deleted: 1}, nil
				})))
		}, WithFulfillerShredder(shredder))

		runErasureFor(t, env, unscopedSubject)

		// Erase-then-fail-to-shred would leave the rows gone and every backup
		// readable until a retry succeeded, which is the gap this feature
		// exists to close. Shred-then-fail leaves noise and a retryable delete.
		test.True(t, shreddedFirst.Load())
	})

	T.Run("fails the request when the key cannot be destroyed", func(t *testing.T) {
		t.Parallel()

		var ran atomic.Int64

		shredder := &recordingShredder{err: platformerrors.New("kms unreachable")}
		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(3, 0, nil, &ran)))
		}, WithFulfillerShredder(shredder))

		req := saveRequest(t, env.store,
			newRequest(identifiers.New(), RequestErasure, unscopedSubject, env.clock.read()))

		// Run as the final attempt, because a shred that cannot reach the KMS is
		// retryable — and it is the row's account of the last one that matters.
		_, err := env.run(t, req.ID, RequestErasure, newFinalReporter())
		must.Error(t, err)

		read := env.reread(t, req.ID)

		test.EqOp(t, StatusFailed, read.Status)
		test.StrContains(t, read.LastError, "kms unreachable")
		test.Nil(t, read.KeyShreddedAt)

		// Nothing was deleted either. An erasure that cannot reach backups is
		// retried whole rather than half-applied.
		test.EqOp(t, int64(0), ran.Load())
	})

	T.Run("records the destruction even when the erasure then fails", func(t *testing.T) {
		t.Parallel()

		shredder := &recordingShredder{}
		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity",
				EraserFunc(func(context.Context, database.SQLQueryExecutor, Subject) (ErasureOutcome, error) {
					return ErasureOutcome{}, platformerrors.New("the ninth domain timed out")
				})))
		}, WithFulfillerShredder(shredder))

		req := runErasureFor(t, env, unscopedSubject)

		test.EqOp(t, StatusInProgress, req.Status)

		// The key is gone whatever happens next, so the row has to say so
		// before the request has finished. Recording it only at completion
		// would leave a subject whose ciphertext is noise and whose rows are
		// still there with nothing anywhere saying why.
		must.NotNil(t, req.KeyShreddedAt)
		test.EqOp(t, baseTime, req.KeyShreddedAt.UTC())
	})

	T.Run("does not move the destruction time on a retry", func(t *testing.T) {
		t.Parallel()

		var attempts atomic.Int64

		shredder := &recordingShredder{}
		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity",
				EraserFunc(func(context.Context, database.SQLQueryExecutor, Subject) (ErasureOutcome, error) {
					if attempts.Add(1) == 1 {
						return ErasureOutcome{}, platformerrors.New("the ninth domain timed out")
					}

					return ErasureOutcome{Deleted: 1}, nil
				})))
		}, WithFulfillerShredder(shredder))

		req := runErasureFor(t, env, unscopedSubject)
		must.EqOp(t, StatusInProgress, req.Status)

		// The retry re-shreds and is told the original destruction time. The
		// column has to keep saying when the key stopped existing, not when
		// somebody last asked about it.
		env.clock.advance(time.Hour)

		_, err := env.run(t, req.ID, RequestErasure, newRecordingReporter(
			operations.Attempt{ID: "op-1", Number: 2}))
		must.NoError(t, err)

		read := env.reread(t, req.ID)

		test.EqOp(t, StatusCompleted, read.Status)
		must.NotNil(t, read.KeyShreddedAt)
		test.EqOp(t, baseTime, read.KeyShreddedAt.UTC())
	})

	T.Run("survives a Worker with no shredder", func(t *testing.T) {
		t.Parallel()

		var ran atomic.Int64

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(3, 0, nil, &ran)))
		})

		req := runErasureFor(t, env, unscopedSubject)

		test.EqOp(t, StatusCompleted, req.Status)
		test.Nil(t, req.KeyShreddedAt)
		test.EqOp(t, int64(1), ran.Load())
	})
}

func TestFulfiller_ShredScoped(T *testing.T) {
	T.Parallel()

	T.Run("skips a scoped request and says why", func(t *testing.T) {
		t.Parallel()

		shredder := &recordingShredder{}
		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(3, 0, nil, nil)))
		}, WithFulfillerShredder(shredder))

		// Scope confines an erasure to one tenant; a data key covers every
		// scope its subject appears in. Destroying it would erase that person's
		// data inside tenants nobody asked about.
		read := runErasureFor(t, env, Subject{ID: "user-1", Type: SubjectUser, Scope: "account-1"})

		test.EqOp(t, StatusCompleted, read.Status)
		test.SliceEmpty(t, shredder.subjects)
		test.Nil(t, read.KeyShreddedAt)

		// Not silently: Retained already means "what was kept and on what
		// basis", which is exactly what an unshreddable key is.
		basis, ok := read.Retained[shredRetentionKey]
		must.True(t, ok)
		test.StrContains(t, basis, "one scope")
	})
}
