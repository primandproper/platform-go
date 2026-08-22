package dataprivacy

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/compression"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/operations"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// fulfillerEnv is a Fulfiller wired over a live store and an in-memory bucket.
type fulfillerEnv struct {
	store     Store
	fulfiller *Fulfiller
	uploader  *memoryUploader
	registry  *Registry
	clock     *stubClock

	// reporter is the one the last run used, so a test can read the progress
	// tiers back without threading it through every call.
	reporter *recordingReporter
}

// newFulfillerEnv builds a Fulfiller with the given registrations.
func newFulfillerEnv(t *testing.T, register func(*Registry), opts ...FulfillerOption) *fulfillerEnv {
	t.Helper()

	env := newSQLiteEnv(t)
	store := env.newStore(t)
	uploader := newMemoryUploader()
	registry := NewRegistry()
	stub := newStubClock()

	register(registry)

	base := []FulfillerOption{
		WithFulfillerUploadManager(uploader),
		WithFulfillerClock(stub),
	}

	fulfiller, err := NewFulfiller(t.Context(), &FulfillerConfig{}, store, registry, append(base, opts...)...)
	must.NoError(t, err)

	return &fulfillerEnv{store: store, fulfiller: fulfiller, uploader: uploader, registry: registry, clock: stub}
}

// submitAndRun saves a request and runs the kind that fulfills it, on what the
// runner is told is its final attempt — so a failure lands on the row rather
// than being left for a retry that no operations worker is here to make.
func (e *fulfillerEnv) submitAndRun(t *testing.T, requestType RequestType) *Request {
	t.Helper()

	req := saveRequest(t, e.store, newRequest(identifiers.New(), requestType, testSubject, e.clock.read()))

	e.run(t, req.ID, requestType, newFinalReporter())

	read, err := e.store.Get(t.Context(), req.ID)
	must.NoError(t, err)

	return read
}

// run drives one attempt at one request and returns what the runner reported.
func (e *fulfillerEnv) run(
	t *testing.T,
	requestID string,
	requestType RequestType,
	rep *recordingReporter,
) (*operations.Result, error) {
	t.Helper()

	e.reporter = rep

	job := Job{RequestID: requestID}

	if requestType == RequestErasure {
		return e.fulfiller.runErasure(t.Context(), job, rep)
	}

	return e.fulfiller.runExport(t.Context(), job, rep)
}

// reread reads a request back after a run.
func (e *fulfillerEnv) reread(t *testing.T, requestID string) *Request {
	t.Helper()

	read, err := e.store.Get(t.Context(), requestID)
	must.NoError(t, err)

	return read
}

func TestFulfiller_Export(T *testing.T) {
	T.Parallel()

	T.Run("assembles every section into the artifact", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"email":"a@example.com"}`)))
			must.NoError(t, r.RegisterCollector("billing", staticCollector(`{"invoices":2}`)))
		})

		req := env.submitAndRun(t, RequestExport)

		test.EqOp(t, StatusCompleted, req.Status)
		test.False(t, req.Partial())
		must.StrNotEqFold(t, "", req.ArtifactRef)
		test.Greater(t, int64(0), req.ArtifactBytes)

		stored, ok := env.uploader.get(req.ArtifactRef)
		must.True(t, ok)

		doc := decodeArtifact(t, &env.fulfiller.packager, stored)

		test.EqOp(t, DocumentFormat, doc.Manifest.Format)
		test.EqOp(t, req.ID, doc.Manifest.RequestID)
		test.EqOp(t, testSubject.ID, doc.Manifest.Subject.ID)
		test.Eq(t, []string{"billing", "identity"}, doc.Manifest.Sections)
		test.MapLen(t, 2, doc.Data)
		test.MapEmpty(t, doc.Manifest.Failures)

		// The fragment reaches the artifact as the collector returned it.
		test.EqOp(t, `{"invoices":2}`, string(mustCompact(t, doc.Data["billing"])))
	})

	T.Run("a failing collector costs its section, not the export", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"email":"a@example.com"}`)))
			must.NoError(t, r.RegisterCollector("billing", failingCollector(platformerrors.New("billing is down"))))
		})

		req := env.submitAndRun(t, RequestExport)

		// Delivered, not failed. A subject with thirty days to complain is
		// better served by most of their data plus an honest account of the gap.
		test.EqOp(t, StatusCompleted, req.Status)
		test.True(t, req.Partial())
		must.MapLen(t, 1, req.Failures)
		test.StrContains(t, req.Failures["billing"], "billing is down")

		stored, ok := env.uploader.get(req.ArtifactRef)
		must.True(t, ok)

		doc := decodeArtifact(t, &env.fulfiller.packager, stored)

		// The manifest names the gap, so the document does not silently assert
		// that the missing data does not exist.
		test.Eq(t, []string{"identity"}, doc.Manifest.Sections)
		must.MapLen(t, 1, doc.Manifest.Failures)
		test.StrContains(t, doc.Manifest.Failures["billing"], "billing is down")
	})

	T.Run("an export where every collector failed is a hard failure", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", failingCollector(platformerrors.New("nope"))))
			must.NoError(t, r.RegisterCollector("billing", failingCollector(platformerrors.New("nope"))))
		})

		req := saveRequest(t, env.store, newRequest(identifiers.New(), RequestExport, testSubject, env.clock.read()))

		_, err := env.run(t, req.ID, RequestExport, newRecordingReporter(
			operations.Attempt{ID: "op-1", Number: 1}))

		// A file asserting that nothing is held about a person is the one wrong
		// answer available here, so nothing is written at all.
		must.Error(t, err)
		test.ErrorIs(t, err, ErrEverySectionFailed)
		test.SliceEmpty(t, env.uploader.paths())

		// Retryable, and not the final attempt, so the row still says the
		// operation is working on it — which it is.
		read := env.reread(t, req.ID)
		test.EqOp(t, StatusInProgress, read.Status)
		test.EqOp(t, "", read.LastError)
	})

	T.Run("a collector returning nothing omits its section", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"email":"a@example.com"}`)))
			must.NoError(t, r.RegisterCollector("billing", CollectorFunc(
				func(context.Context, Subject) (json.RawMessage, error) { return nil, nil },
			)))
		})

		req := env.submitAndRun(t, RequestExport)

		test.EqOp(t, StatusCompleted, req.Status)
		test.False(t, req.Partial())

		stored, _ := env.uploader.get(req.ArtifactRef)
		doc := decodeArtifact(t, &env.fulfiller.packager, stored)

		// "Nothing about this subject" is a complete answer, and omitting the
		// section says so. Writing null would claim the domain holds a null.
		test.Eq(t, []string{"identity"}, doc.Manifest.Sections)
		test.MapLen(t, 1, doc.Data)
	})

	T.Run("a collector panic becomes that section's failure", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
			must.NoError(t, r.RegisterCollector("billing", CollectorFunc(
				func(context.Context, Subject) (json.RawMessage, error) { panic("boom") },
			)))
		})

		req := env.submitAndRun(t, RequestExport)

		// Somebody else's code running in our goroutine: a nil map access in
		// one domain costs that domain's section, not the whole batch.
		test.EqOp(t, StatusCompleted, req.Status)
		must.MapLen(t, 1, req.Failures)
		test.StrContains(t, req.Failures["billing"], "panicked")
	})

	T.Run("a collector returning malformed JSON fails only its section", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
			must.NoError(t, r.RegisterCollector("billing", staticCollector(`{"broken":`)))
		})

		req := env.submitAndRun(t, RequestExport)

		// Caught before assembly. A malformed fragment reaching the artifact
		// would make the whole document unparseable — one domain's bug becoming
		// a total loss.
		test.EqOp(t, StatusCompleted, req.Status)
		must.MapLen(t, 1, req.Failures)
		test.StrContains(t, req.Failures["billing"], "invalid JSON")

		stored, _ := env.uploader.get(req.ArtifactRef)

		var doc Document
		decoded, err := env.fulfiller.packager.decode(t.Context(), stored, testRequestID)
		must.NoError(t, err)
		test.NoError(t, json.Unmarshal(decoded, &doc))
	})

	T.Run("the artifact is stored uncacheable", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		})

		req := env.submitAndRun(t, RequestExport)

		// A cache between the bucket and the subject would keep serving the
		// object after the link expired and after the sweeper deleted it.
		env.uploader.mu.Lock()
		defer env.uploader.mu.Unlock()

		test.EqOp(t, "application/json", env.uploader.types[req.ArtifactRef])
	})

	T.Run("compression round trips", func(t *testing.T) {
		t.Parallel()

		compressor, err := compression.NewCompressor(compression.AlgorithmZstd)
		must.NoError(t, err)

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"email":"a@example.com"}`)))
		}, WithFulfillerCompressor(compressor))

		req := env.submitAndRun(t, RequestExport)

		stored, ok := env.uploader.get(req.ArtifactRef)
		must.True(t, ok)

		doc := decodeArtifact(t, &env.fulfiller.packager, stored)
		test.Eq(t, []string{"identity"}, doc.Manifest.Sections)
	})

	T.Run("an oversized document fails without being stored", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"padding":"aaaaaaaaaaaaaaaaaaaa"}`)))
		})

		env.fulfiller.cfg.MaxDocumentBytes = 8

		req := saveRequest(t, env.store, newRequest(identifiers.New(), RequestExport, testSubject, env.clock.read()))

		// Unretryable, so the row is failed on the first attempt rather than
		// waiting for a budget to run out on a document that will be the same
		// size every time.
		_, err := env.run(t, req.ID, RequestExport, newRecordingReporter(
			operations.Attempt{ID: "op-1", Number: 1}))

		must.Error(t, err)
		test.True(t, operations.IsUnretryable(err))

		read := env.reread(t, req.ID)
		test.EqOp(t, StatusFailed, read.Status)
		test.StrContains(t, read.LastError, "exceeds configured maximum")
		test.SliceEmpty(t, env.uploader.paths())
	})
}

func TestFulfiller_Erasure(T *testing.T) {
	T.Parallel()

	T.Run("sums outcomes and namespaces retentions", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity",
				countingEraser(5, 1, nil, nil)))
			must.NoError(t, r.RegisterEraser("billing",
				countingEraser(2, 3, map[string]string{"invoices": "tax law"}, nil)))
		})

		req := env.submitAndRun(t, RequestErasure)

		test.EqOp(t, StatusCompleted, req.Status)
		test.EqOp(t, int64(7), req.Deleted)
		test.EqOp(t, int64(4), req.Anonymized)

		// Namespaced by eraser key so two domains retaining "invoices" for
		// different reasons do not overwrite each other.
		must.MapLen(t, 1, req.Retained)
		test.EqOp(t, "tax law", req.Retained["billing.invoices"])
	})

	T.Run("one eraser failing rolls the whole erasure back", func(t *testing.T) {
		t.Parallel()

		var ranFirst atomic.Int64

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("aaa", countingEraser(5, 0, nil, &ranFirst)))
			must.NoError(t, r.RegisterEraser("zzz", EraserFunc(
				func(context.Context, database.SQLQueryExecutor, Subject) (ErasureOutcome, error) {
					return ErasureOutcome{}, platformerrors.New("cannot reach billing")
				},
			)))
		})

		req := env.submitAndRun(t, RequestErasure)

		// A subject deleted from one domain and present in another has not been
		// erased and cannot be told they have.
		test.EqOp(t, StatusFailed, req.Status)
		test.StrContains(t, req.LastError, "cannot reach billing")
		test.EqOp(t, int64(0), req.Deleted)
		test.EqOp(t, int64(0), req.Anonymized)

		// The first eraser did run — its work is undone by the rollback, not by
		// never having happened.
		test.EqOp(t, int64(1), ranFirst.Load())
	})

	T.Run("an eraser panic aborts the erasure", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", EraserFunc(
				func(context.Context, database.SQLQueryExecutor, Subject) (ErasureOutcome, error) {
					panic("boom")
				},
			)))
		})

		req := env.submitAndRun(t, RequestErasure)

		test.EqOp(t, StatusFailed, req.Status)
		test.StrContains(t, req.LastError, "panicked")
	})

	T.Run("an erasure with no registered eraser fails terminally", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{}`)))
		})

		req := env.submitAndRun(t, RequestErasure)

		// An erasure that erases nothing while reporting success is the worst
		// failure available here, because nobody goes looking for it.
		test.EqOp(t, StatusFailed, req.Status)
		test.StrContains(t, req.LastError, "no dataprivacy erasers registered")
	})
}

// The retry loop is the operations worker's now, so what is left to test here
// is the one thing this package still decides about a failure: whether it is
// final, and therefore whether the row and the subject are told about it.
func TestFulfiller_Failure(T *testing.T) {
	T.Parallel()

	T.Run("a non-final attempt leaves the row in progress", func(t *testing.T) {
		t.Parallel()

		var notified atomic.Int64

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		}, WithFulfillerNotifier(NotifierFunc(func(context.Context, *Notification) error {
			notified.Add(1)

			return nil
		})))

		env.fulfiller.uploader = &failingSaveUploader{memoryUploader: env.uploader}

		req := saveRequest(t, env.store, newRequest(identifiers.New(), RequestExport, testSubject, env.clock.read()))

		_, err := env.run(t, req.ID, RequestExport, newRecordingReporter(
			operations.Attempt{ID: "op-1", Number: 1, Final: false}))
		must.Error(t, err)

		// A row marked failed while the operation is going to try again would be
		// read by the overdue gauge, by the sweeper, and by the subject's status
		// page as a request that had been given up on.
		read := env.reread(t, req.ID)
		test.EqOp(t, StatusInProgress, read.Status)
		test.EqOp(t, "", read.LastError)
		test.EqOp(t, int64(0), notified.Load())
	})

	T.Run("the final attempt fails the row and notifies the subject", func(t *testing.T) {
		t.Parallel()

		var (
			notified   atomic.Int64
			lastStatus atomic.Pointer[Status]
		)

		// An unwritable bucket rather than a failing collector: it is a
		// retryable failure of the whole export, which is the shape this is
		// about, where a failing collector costs only its own section.
		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		}, WithFulfillerNotifier(NotifierFunc(func(_ context.Context, n *Notification) error {
			notified.Add(1)
			lastStatus.Store(&n.Request.Status)

			return nil
		})))

		env.fulfiller.uploader = &failingSaveUploader{memoryUploader: env.uploader}

		req := saveRequest(t, env.store, newRequest(identifiers.New(), RequestExport, testSubject, env.clock.read()))

		_, err := env.run(t, req.ID, RequestExport, newRecordingReporter(
			operations.Attempt{ID: "op-1", Number: DefaultMaxAttempts, Final: true}))
		must.Error(t, err)

		// Somebody is owed an answer and is not going to get one. Telling them
		// beats a status page that says "in progress" until the window runs out
		// — and this is the only moment at which it is a true thing to say.
		read := env.reread(t, req.ID)
		test.EqOp(t, StatusFailed, read.Status)
		test.StrContains(t, read.LastError, "the bucket is read-only")
		test.False(t, read.CompletedAt == nil)

		test.EqOp(t, int64(1), notified.Load())
		must.NotNil(t, lastStatus.Load())
		test.EqOp(t, StatusFailed, *lastStatus.Load())
	})

	T.Run("a final failure against a row that moved on tells nobody", func(t *testing.T) {
		t.Parallel()

		var notified atomic.Int64

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		}, WithFulfillerNotifier(NotifierFunc(func(context.Context, *Notification) error {
			notified.Add(1)

			return nil
		})))

		env.fulfiller.uploader = &failingSaveUploader{memoryUploader: env.uploader}

		// Cancelled while the attempt was running, so by the time it gives up
		// the row already says something truer than "failed".
		req := newRequest(identifiers.New(), RequestExport, testSubject, env.clock.read())
		req.Status = StatusCancelled
		saveRequest(t, env.store, req)

		_, err := env.run(t, req.ID, RequestExport, newFinalReporter())
		must.Error(t, err)

		read := env.reread(t, req.ID)
		test.EqOp(t, StatusCancelled, read.Status)

		// Telling a subject their request failed when it was cancelled is worse
		// than telling them nothing.
		test.EqOp(t, int64(0), notified.Load())
	})

	T.Run("an unretryable failure is final whatever the attempt says", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(0, 0, nil, nil)))
		})

		req := saveRequest(t, env.store, newRequest(identifiers.New(), RequestExport, testSubject, env.clock.read()))

		// Started as an export against a registry with no collectors: the kind
		// and the row disagree, which no retry resolves.
		_, err := env.run(t, req.ID, RequestErasure, newRecordingReporter(
			operations.Attempt{ID: "op-1", Number: 1, Final: false}))

		must.Error(t, err)
		test.True(t, operations.IsUnretryable(err))
	})

	T.Run("a notification failure does not fail the request", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		}, WithFulfillerNotifier(NotifierFunc(func(context.Context, *Notification) error {
			return platformerrors.New("mail server is down")
		})))

		req := env.submitAndRun(t, RequestExport)

		// The export exists; re-running it to retry an email would re-run every
		// collector against the subject's data.
		test.EqOp(t, StatusCompleted, req.Status)
	})
}

// The domains are the unit tier and they cost nothing to count, because the
// registry already enumerates them.
func TestFulfiller_Progress(T *testing.T) {
	T.Parallel()

	T.Run("an export reports domains as units and bytes within them", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"email":"a@example.com"}`)))
			must.NoError(t, r.RegisterCollector("billing", staticCollector(`{"invoices":2}`)))
			must.NoError(t, r.RegisterCollector("webhooks", staticCollector(`{"hooks":[]}`)))
		})

		env.submitAndRun(t, RequestExport)

		total, totalSet, done, count := env.reporter.progress()

		must.True(t, totalSet)
		test.EqOp(t, 3, total)
		test.EqOp(t, 3, done)
		test.Eq(t, []string{"billing", "identity", "webhooks"}, env.reporter.startedUnits())

		// Bytes rather than records: a Collector hands back one opaque fragment
		// and there is nothing inside it this package is entitled to count.
		test.Greater(t, int64(0), count)
	})

	T.Run("a failed domain still finishes its unit", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
			must.NoError(t, r.RegisterCollector("billing", failingCollector(platformerrors.New("down"))))
		})

		req := env.submitAndRun(t, RequestExport)
		must.True(t, req.Partial())

		// The export completes over both domains. Which of them came back empty
		// is the result's business, not the progress bar's.
		total, _, done, _ := env.reporter.progress()
		test.EqOp(t, 2, total)
		test.EqOp(t, 2, done)
	})

	T.Run("an erasure reports erasers as units and rows within them", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(5, 1, nil, nil)))
			must.NoError(t, r.RegisterEraser("billing", countingEraser(2, 3, nil, nil)))
		})

		env.submitAndRun(t, RequestErasure)

		total, totalSet, done, count := env.reporter.progress()

		must.True(t, totalSet)
		test.EqOp(t, 2, total)
		test.EqOp(t, 2, done)
		test.EqOp(t, int64(11), count)
	})
}

// Cancellation is a request the runner answers between units, and the row it
// leaves behind has to say what happened.
func TestFulfiller_Cancellation(T *testing.T) {
	T.Parallel()

	T.Run("an export asked to stop writes nothing and marks the row", func(t *testing.T) {
		t.Parallel()

		var collected atomic.Int64

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", CollectorFunc(
				func(context.Context, Subject) (json.RawMessage, error) {
					collected.Add(1)

					return json.RawMessage(`{"ok":true}`), nil
				},
			)))
		})

		req := saveRequest(t, env.store, newRequest(identifiers.New(), RequestExport, testSubject, env.clock.read()))

		rep := newFinalReporter()
		rep.cancel()

		_, err := env.run(t, req.ID, RequestExport, rep)
		must.Error(t, err)

		// Checked before the fan-out, because collection is the expensive half:
		// every domain queries the application's own database on behalf of a
		// request somebody has withdrawn.
		test.EqOp(t, int64(0), collected.Load())
		test.SliceEmpty(t, env.uploader.paths())

		read := env.reread(t, req.ID)
		test.EqOp(t, StatusCancelled, read.Status)
	})

	T.Run("an erasure asked to stop erases nothing", func(t *testing.T) {
		t.Parallel()

		var erased atomic.Int64

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(1, 0, nil, &erased)))
		})

		req := saveRequest(t, env.store, newRequest(identifiers.New(), RequestErasure, testSubject, env.clock.read()))

		rep := newFinalReporter()
		rep.cancel()

		_, err := env.run(t, req.ID, RequestErasure, rep)
		must.Error(t, err)

		// The last instant at which stopping means anything is before the shred
		// and before the transaction, because neither can be half-done.
		test.EqOp(t, int64(0), erased.Load())

		read := env.reread(t, req.ID)
		test.EqOp(t, StatusCancelled, read.Status)
	})
}

// A lease that lapses while its holder is merely slow hands the same operation
// to somebody else, and both run.
func TestFulfiller_DuplicateExecution(T *testing.T) {
	T.Parallel()

	T.Run("a second attempt at a completed request reports the first one's result", func(t *testing.T) {
		t.Parallel()

		var collected atomic.Int64

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", CollectorFunc(
				func(context.Context, Subject) (json.RawMessage, error) {
					collected.Add(1)

					return json.RawMessage(`{"ok":true}`), nil
				},
			)))
		})

		req := env.submitAndRun(t, RequestExport)
		must.EqOp(t, StatusCompleted, req.Status)

		result, err := env.run(t, req.ID, RequestExport, newFinalReporter())
		must.NoError(t, err)
		must.NotNil(t, result)

		// The work is done and the row is the proof. Doing it again would re-run
		// every collector against the subject's data to produce the same bytes
		// at the same key.
		test.EqOp(t, int64(1), collected.Load())
		test.EqOp(t, req.ArtifactRef, result.URI)

		// And the replayed summary is the one the attempt that did the work
		// reported, rather than a thinner version of it.
		var summary ExportSummary
		must.NoError(t, json.Unmarshal(result.Detail, &summary))
		test.EqOp(t, req.ArtifactBytes, summary.Bytes)
	})

	T.Run("a request that left the in-progress state is not run", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		})

		req := newRequest(identifiers.New(), RequestExport, testSubject, env.clock.read())
		req.Status = StatusCancelled
		saveRequest(t, env.store, req)

		_, err := env.run(t, req.ID, RequestExport, newFinalReporter())

		must.Error(t, err)
		test.ErrorIs(t, err, ErrNotInProgress)
		test.True(t, operations.IsUnretryable(err))
		test.SliceEmpty(t, env.uploader.paths())
	})

	T.Run("a request that is not there fails without retrying", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		})

		_, err := env.run(t, "no-such-request", RequestExport, newFinalReporter())

		// Retention swept it, or it never existed. Neither becomes a request by
		// waiting.
		must.Error(t, err)
		test.ErrorIs(t, err, ErrRequestNotFound)
		test.True(t, operations.IsUnretryable(err))
	})
}

func TestFulfiller_Register(T *testing.T) {
	T.Parallel()

	T.Run("registers the kind each half of the registry supports", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{}`)))
			must.NoError(t, r.RegisterEraser("identity", countingEraser(0, 0, nil, nil)))
		})

		registry := operations.NewRegistry()
		must.NoError(t, env.fulfiller.Register(registry))

		test.Eq(t, []string{KindErasure, KindExport}, registry.Kinds())
	})

	T.Run("registers only the kinds it can run", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(0, 0, nil, nil)))
		})

		registry := operations.NewRegistry()
		must.NoError(t, env.fulfiller.Register(registry))

		// A kind whose runner has nothing registered behind it would accept
		// submissions and produce an export of nothing.
		test.Eq(t, []string{KindErasure}, registry.Kinds())
	})

	T.Run("refuses to register twice", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{}`)))
		})

		registry := operations.NewRegistry()
		must.NoError(t, env.fulfiller.Register(registry))

		// A silent overwrite would swap the runner under operations already
		// queued.
		test.ErrorIs(t, env.fulfiller.Register(registry), operations.ErrDuplicateKind)
	})
}

func TestNewArtifactURLSigner(T *testing.T) {
	T.Parallel()

	T.Run("signs a completed export's artifact", func(t *testing.T) {
		t.Parallel()

		uploader := &signingUploader{memoryUploader: newMemoryUploader()}

		sign := NewArtifactURLSigner(uploader, time.Minute, false)

		url, expiresAt := sign(t.Context(), &Request{ArtifactRef: "exports/x.json"})

		test.StrContains(t, url, "exports/x.json")
		test.False(t, expiresAt.IsZero())
	})

	T.Run("stamps the expiry through the clock it was given", func(t *testing.T) {
		t.Parallel()

		uploader := &signingUploader{memoryUploader: newMemoryUploader()}
		c := newStubClock()

		// The expiry is what the subject reads out of the notification, so it has
		// to come from the same clock the Fulfiller stamps everything else with —
		// otherwise a test clock and a wall-clock deadline disagree, and only
		// whoever compares the two ever finds out.
		sign := NewArtifactURLSigner(uploader, time.Minute, false, WithURLSignerClock(c))

		_, expiresAt := sign(t.Context(), &Request{ArtifactRef: "exports/x.json"})

		test.EqOp(t, c.read().UTC().Add(time.Minute), expiresAt)
	})

	T.Run("declines when artifacts are encrypted", func(t *testing.T) {
		t.Parallel()

		uploader := &signingUploader{memoryUploader: newMemoryUploader()}

		// The same refusal Service.Download makes, for the same reason: the
		// subject would receive base64 ciphertext.
		sign := NewArtifactURLSigner(uploader, time.Minute, true)

		url, expiresAt := sign(t.Context(), &Request{ArtifactRef: "exports/x.json"})

		test.EqOp(t, "", url)
		test.True(t, expiresAt.IsZero())
	})

	T.Run("declines when the provider cannot sign", func(t *testing.T) {
		t.Parallel()

		sign := NewArtifactURLSigner(newMemoryUploader(), time.Minute, false)

		url, _ := sign(t.Context(), &Request{ArtifactRef: "exports/x.json"})
		test.EqOp(t, "", url)
	})

	T.Run("declines a request with no artifact", func(t *testing.T) {
		t.Parallel()

		uploader := &signingUploader{memoryUploader: newMemoryUploader()}

		sign := NewArtifactURLSigner(uploader, time.Minute, false)

		url, _ := sign(t.Context(), &Request{})
		test.EqOp(t, "", url)
	})

	T.Run("reaches the notification", func(t *testing.T) {
		t.Parallel()

		uploader := &signingUploader{memoryUploader: newMemoryUploader()}

		var deliveredURL atomic.Pointer[string]

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		},
			WithFulfillerUploadManager(uploader),
			WithFulfillerURLSigner(NewArtifactURLSigner(uploader, time.Minute, false)),
			WithFulfillerNotifier(NotifierFunc(func(_ context.Context, n *Notification) error {
				deliveredURL.Store(&n.DownloadURL)

				return nil
			})),
		)

		req := env.submitAndRun(t, RequestExport)

		must.NotNil(t, deliveredURL.Load())
		test.StrContains(t, *deliveredURL.Load(), req.ArtifactRef)
	})
}

func TestNewFulfiller(T *testing.T) {
	T.Parallel()

	T.Run("refuses an empty registry", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		_, err := NewFulfiller(t.Context(), &FulfillerConfig{}, env.newStore(t), NewRegistry(),
			WithFulfillerUploadManager(newMemoryUploader()))
		test.ErrorIs(t, err, ErrNoCollectors)
	})

	T.Run("refuses collectors with no storage", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		registry := NewRegistry()
		must.NoError(t, registry.RegisterCollector("identity", staticCollector(`{}`)))

		// A fulfiller that collects eleven domains and then discovers it has
		// nowhere to write has already done all the expensive work.
		_, err := NewFulfiller(t.Context(), &FulfillerConfig{}, env.newStore(t), registry)
		test.ErrorIs(t, err, ErrNoUploadManager)
	})

	T.Run("an erasure-only fulfiller needs no storage", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		registry := NewRegistry()
		must.NoError(t, registry.RegisterEraser("identity", countingEraser(0, 0, nil, nil)))

		_, err := NewFulfiller(t.Context(), &FulfillerConfig{}, env.newStore(t), registry)
		test.NoError(t, err)
	})

	T.Run("refuses a collector timeout that outlasts the whole attempt", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		registry := NewRegistry()
		must.NoError(t, registry.RegisterEraser("identity", countingEraser(0, 0, nil, nil)))

		// The reverse ordering produces an export that times out while its first
		// domain is still within its own allowance, which reads as a broken
		// collector rather than a mis-sized config.
		_, err := NewFulfiller(t.Context(), &FulfillerConfig{
			FulfillmentTimeout: time.Minute,
			CollectorTimeout:   time.Hour,
		}, env.newStore(t), registry)

		must.Error(t, err)
		test.StrContains(t, err.Error(), "must exceed collector timeout")
	})
}

// mustCompact normalizes JSON for comparison.
func mustCompact(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()

	compacted, err := json.Marshal(raw)
	must.NoError(t, err)

	return compacted
}
