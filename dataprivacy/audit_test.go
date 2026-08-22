package dataprivacy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/audit"
	auditmock "github.com/primandproper/platform-go/v13/audit/mock"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingAudit captures the entries this package writes.
type recordingAudit struct {
	*auditmock.RecorderMock

	err     error
	entries []*audit.Entry
	mu      sync.Mutex
}

func newRecordingAudit() *recordingAudit {
	r := &recordingAudit{}

	r.RecorderMock = &auditmock.RecorderMock{
		RecordFunc: func(_ context.Context, _ database.SQLQueryExecutor, entries ...*audit.Entry) error {
			r.mu.Lock()
			defer r.mu.Unlock()

			if r.err != nil {
				return r.err
			}

			r.entries = append(r.entries, entries...)

			return nil
		},
	}

	return r
}

func (r *recordingAudit) all() []*audit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]*audit.Entry(nil), r.entries...)
}

func (r *recordingAudit) last() *audit.Entry {
	entries := r.all()
	if len(entries) == 0 {
		return nil
	}

	return entries[len(entries)-1]
}

// auditServiceEnv is a Service wired to a recording audit log.
type auditServiceEnv struct {
	svc      Service
	store    Store
	recorder *recordingAudit
	uploader *signingUploader
}

func newAuditServiceEnv(t *testing.T, cfg *ServiceConfig, opts ...ServiceOption) *auditServiceEnv {
	t.Helper()

	env := newSQLiteEnv(t)
	store := env.newStore(t)
	recorder := newRecordingAudit()
	uploader := &signingUploader{memoryUploader: newMemoryUploader()}

	base := []ServiceOption{
		WithServiceClock(newStubClock()),
		WithServiceAuditRecorder(recorder),
		WithServiceUploadManager(uploader),
		WithActorResolver(func(context.Context) audit.Actor {
			return audit.Actor{ID: "agent-7", Type: audit.ActorUser, IP: "203.0.113.9"}
		}),
	}

	svc, err := NewService(t.Context(), cfg, store, newStubOperations(), append(base, opts...)...)
	must.NoError(t, err)

	return &auditServiceEnv{svc: svc, store: store, recorder: recorder, uploader: uploader}
}

func TestService_AuditRecording(T *testing.T) {
	T.Parallel()

	T.Run("a submission is recorded", func(t *testing.T) {
		t.Parallel()

		env := newAuditServiceEnv(t, &ServiceConfig{})

		req, err := env.svc.Submit(t.Context(), testSubject, RequestExport)
		must.NoError(t, err)

		entry := env.recorder.last()
		must.NotNil(t, entry)

		test.EqOp(t, audit.EventCreated, entry.EventType)
		test.EqOp(t, auditResourceType, entry.ResourceType)
		test.EqOp(t, req.ID, entry.ResourceID)

		// The scope is the subject's tenancy boundary, so a tenant's audit
		// chain covers requests made about its own people.
		test.EqOp(t, testSubject.Scope, entry.Scope)

		// The actor is who is acting, which is not the subject: a support agent
		// running an export for a customer is the event worth recording.
		test.EqOp(t, "agent-7", entry.Actor.ID)
		test.EqOp(t, audit.ActorUser, entry.Actor.Type)

		test.EqOp(t, string(RequestExport), entry.Metadata["request_type"])
		test.EqOp(t, testSubject.ID, entry.Metadata["subject_id"])
	})

	T.Run("the entry carries nothing about the subject but their ID", func(t *testing.T) {
		t.Parallel()

		env := newAuditServiceEnv(t, &ServiceConfig{})

		_, err := env.svc.Submit(t.Context(), testSubject, RequestExport)
		must.NoError(t, err)

		entry := env.recorder.last()
		must.NotNil(t, entry)

		// An audit log is durable by design, and copying a person's data into
		// the log that records the request to export it would defeat both.
		test.MapEmpty(t, entry.Changes)

		for key := range entry.Metadata {
			test.SliceContains(t,
				[]string{"request_type", "status", "subject_id", "subject_type", "reason"},
				key,
				test.Sprintf("unexpected metadata key %q", key),
			)
		}
	})

	T.Run("without a recorder nothing is recorded and nothing breaks", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		svc, err := NewService(t.Context(), &ServiceConfig{}, store, newStubOperations(), WithServiceClock(newStubClock()))
		must.NoError(t, err)

		_, err = svc.Submit(t.Context(), testSubject, RequestExport)
		test.NoError(t, err)
	})

	T.Run("a failing recorder rolls the submission back", func(t *testing.T) {
		t.Parallel()

		env := newAuditServiceEnv(t, &ServiceConfig{})

		env.recorder.mu.Lock()
		env.recorder.err = platformerrors.New("audit chain is locked")
		env.recorder.mu.Unlock()

		req, err := env.svc.Submit(t.Context(), testSubject, RequestExport)
		must.Error(t, err)
		test.Nil(t, req)

		// This is the whole reason Save takes the caller's executor: a request
		// that commits without a record of who asked is a data-export path with
		// no alarm on it.
		results, err := env.store.List(t.Context(), testSubject, filtering.DefaultQueryFilter())
		must.NoError(t, err)
		test.SliceEmpty(t, results.Data)
	})

	T.Run("confirm and cancel are recorded with a reason", func(t *testing.T) {
		t.Parallel()

		env := newAuditServiceEnv(t, &ServiceConfig{ConfirmationWindow: 72 * time.Hour})

		confirmed, err := env.svc.Submit(t.Context(), testSubject, RequestErasure)
		must.NoError(t, err)

		_, err = env.svc.Confirm(t.Context(), confirmed.ID)
		must.NoError(t, err)

		entry := env.recorder.last()
		must.NotNil(t, entry)
		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, "confirmed", entry.Metadata["reason"])
		test.EqOp(t, string(StatusInProgress), entry.Metadata["status"])

		cancelled, err := env.svc.Submit(t.Context(), testSubject, RequestErasure)
		must.NoError(t, err)

		_, err = env.svc.Cancel(t.Context(), cancelled.ID)
		must.NoError(t, err)

		entry = env.recorder.last()
		must.NotNil(t, entry)
		test.StrContains(t, entry.Metadata["reason"], "cancelled")
		test.EqOp(t, string(StatusCancelled), entry.Metadata["status"])
	})

	T.Run("issuing a download URL is recorded as an access", func(t *testing.T) {
		t.Parallel()

		env := newAuditServiceEnv(t, &ServiceConfig{})

		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		req.Status = StatusCompleted
		req.ArtifactRef = "dataprivacy/exports/" + req.ID + ".json"
		saveRequest(t, env.store, req)

		must.NoError(t, env.uploader.Save(t.Context(), req.ArtifactRef, stringReader(`{"data":{}}`)))

		_, err := env.svc.Download(t.Context(), req.ID)
		must.NoError(t, err)

		entry := env.recorder.last()
		must.NotNil(t, entry)

		// Minting the link is the moment the data becomes reachable, and it is
		// the event an investigation asks about.
		test.EqOp(t, audit.EventAccessed, entry.EventType)
		test.EqOp(t, "download_url_issued", entry.Metadata["action"])
	})

	T.Run("reading an artifact is recorded as an access", func(t *testing.T) {
		t.Parallel()

		env := newAuditServiceEnv(t, &ServiceConfig{})

		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		req.Status = StatusCompleted
		req.ArtifactRef = "dataprivacy/exports/" + req.ID + ".json"
		saveRequest(t, env.store, req)

		must.NoError(t, env.uploader.Save(t.Context(), req.ArtifactRef, stringReader(`{"data":{}}`)))

		reader, err := env.svc.Open(t.Context(), req.ID)
		must.NoError(t, err)
		must.NoError(t, reader.Close())

		entry := env.recorder.last()
		must.NotNil(t, entry)
		test.EqOp(t, audit.EventAccessed, entry.EventType)
		test.EqOp(t, "artifact_read", entry.Metadata["action"])
	})

	T.Run("a failed access record does not fail the download", func(t *testing.T) {
		t.Parallel()

		env := newAuditServiceEnv(t, &ServiceConfig{})

		req := newRequest(identifiers.New(), RequestExport, testSubject, baseTime)
		req.Status = StatusCompleted
		req.ArtifactRef = "dataprivacy/exports/" + req.ID + ".json"
		saveRequest(t, env.store, req)

		must.NoError(t, env.uploader.Save(t.Context(), req.ArtifactRef, stringReader(`{"data":{}}`)))

		env.recorder.mu.Lock()
		env.recorder.err = platformerrors.New("audit chain is locked")
		env.recorder.mu.Unlock()

		// The read already happened by the time the record is attempted, so
		// failing the call would report an error for something that succeeded.
		url, err := env.svc.Download(t.Context(), req.ID)
		test.NoError(t, err)
		test.StrContains(t, url, req.ArtifactRef)
	})

	T.Run("the default actor is the system", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)
		recorder := newRecordingAudit()

		svc, err := NewService(t.Context(), &ServiceConfig{}, store, newStubOperations(),
			WithServiceClock(newStubClock()),
			WithServiceAuditRecorder(recorder),
		)
		must.NoError(t, err)

		_, err = svc.Submit(t.Context(), testSubject, RequestExport)
		must.NoError(t, err)

		entry := recorder.last()
		must.NotNil(t, entry)

		// Honest for a self-service portal, misleading for a staff tool — which
		// is why WithActorResolver exists.
		test.EqOp(t, serviceName, entry.Actor.ID)
		test.EqOp(t, audit.ActorSystem, entry.Actor.Type)
	})
}

func TestFulfiller_AuditRecording(T *testing.T) {
	T.Parallel()

	T.Run("a completed export is recorded with what it disclosed", func(t *testing.T) {
		t.Parallel()

		recorder := newRecordingAudit()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"email":"a@example.com"}`)))
			must.NoError(t, r.RegisterCollector("billing", failingCollector(platformerrors.New("down"))))
		}, WithFulfillerAuditRecorder(recorder))

		req := env.submitAndRun(t, RequestExport)
		must.EqOp(t, StatusCompleted, req.Status)

		entry := recorder.last()
		must.NotNil(t, entry)

		test.EqOp(t, audit.EventUpdated, entry.EventType)
		test.EqOp(t, req.ID, entry.ResourceID)
		test.EqOp(t, "1", entry.Metadata["sections"])
		test.EqOp(t, "1", entry.Metadata["failed_sections"])
		test.EqOp(t, string(StatusCompleted), entry.Metadata["status"])

		// The size is recorded; the contents are not.
		test.StrNotContains(t, entry.Metadata["artifact_bytes"], "a@example.com")
	})

	T.Run("a completed erasure is recorded with what it destroyed", func(t *testing.T) {
		t.Parallel()

		recorder := newRecordingAudit()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity",
				countingEraser(9, 2, map[string]string{"invoices": "tax law"}, nil)))
		}, WithFulfillerAuditRecorder(recorder))

		req := env.submitAndRun(t, RequestErasure)
		must.EqOp(t, StatusCompleted, req.Status)

		entry := recorder.last()
		must.NotNil(t, entry)

		test.EqOp(t, "9", entry.Metadata["deleted"])
		test.EqOp(t, "2", entry.Metadata["anonymized"])
		test.EqOp(t, "1", entry.Metadata["retained"])
	})

	T.Run("a failing recorder rolls the erasure back", func(t *testing.T) {
		t.Parallel()

		recorder := newRecordingAudit()
		recorder.err = platformerrors.New("audit chain is locked")

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(9, 0, nil, nil)))
		}, WithFulfillerAuditRecorder(recorder))

		req := env.submitAndRun(t, RequestErasure)

		// The erasure and its record share one transaction, so an unrecordable
		// erasure does not happen at all.
		test.EqOp(t, StatusFailed, req.Status)
		test.EqOp(t, int64(0), req.Deleted)
		test.StrContains(t, req.LastError, "audit chain is locked")
	})

	T.Run("the fulfiller's default actor is the system", func(t *testing.T) {
		t.Parallel()

		recorder := newRecordingAudit()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(1, 0, nil, nil)))
		}, WithFulfillerAuditRecorder(recorder))

		env.submitAndRun(t, RequestErasure)

		entry := recorder.last()
		must.NotNil(t, entry)

		// A background job belongs to no user, and ActorSystem with the job's
		// name is the honest rendering of that.
		test.EqOp(t, serviceName, entry.Actor.ID)
		test.EqOp(t, audit.ActorSystem, entry.Actor.Type)
	})

	T.Run("a custom actor resolver reaches the entry", func(t *testing.T) {
		t.Parallel()

		recorder := newRecordingAudit()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterEraser("identity", countingEraser(1, 0, nil, nil)))
		},
			WithFulfillerAuditRecorder(recorder),
			WithFulfillerActorResolver(func(context.Context) audit.Actor {
				return audit.Actor{ID: "erasure-worker-3", Type: audit.ActorService}
			}),
		)

		env.submitAndRun(t, RequestErasure)

		entry := recorder.last()
		must.NotNil(t, entry)
		test.EqOp(t, "erasure-worker-3", entry.Actor.ID)
		test.EqOp(t, audit.ActorService, entry.Actor.Type)
	})
}

func TestService_List(T *testing.T) {
	T.Parallel()

	T.Run("returns a subject's requests", func(t *testing.T) {
		t.Parallel()

		svc, _, _ := newTestService(t, &ServiceConfig{})

		first, err := svc.Submit(t.Context(), testSubject, RequestExport)
		must.NoError(t, err)

		second, err := svc.Submit(t.Context(), testSubject, RequestErasure)
		must.NoError(t, err)

		results, err := svc.List(t.Context(), testSubject, filtering.DefaultQueryFilter())
		must.NoError(t, err)
		must.SliceLen(t, 2, results.Data)

		test.EqOp(t, first.ID, results.Data[0].ID)
		test.EqOp(t, second.ID, results.Data[1].ID)
	})

	T.Run("a nil filter is defaulted", func(t *testing.T) {
		t.Parallel()

		svc, _, _ := newTestService(t, &ServiceConfig{})

		_, err := svc.Submit(t.Context(), testSubject, RequestExport)
		must.NoError(t, err)

		results, err := svc.List(t.Context(), testSubject, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, results.Data)
	})

	T.Run("rejects an empty subject", func(t *testing.T) {
		t.Parallel()

		svc, _, _ := newTestService(t, &ServiceConfig{})

		_, err := svc.List(t.Context(), Subject{}, nil)
		test.True(t, errors.Is(err, ErrEmptySubjectID))
	})
}
