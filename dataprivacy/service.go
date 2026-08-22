package dataprivacy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/uploads"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// auditResourceType is what this package's audit entries are about.
const auditResourceType = "dataprivacy_request"

var _ Service = (*StoreService)(nil)

// StoreService is the request state machine, over a Store and an operations
// Service. It is exported, and returned by NewService, so a caller can depend on
// the service it built rather than on the Service seam.
type StoreService struct {
	store      Store
	operations operations.Service
	clock      clock.Clock
	o11y       observability.Observer
	uploader   uploads.UploadManager
	recorder   audit.Recorder
	actor      ActorResolver

	packager packager

	submittedCounter metrics.Int64Counter
	confirmedCounter metrics.Int64Counter
	cancelledCounter metrics.Int64Counter
	downloadCounter  metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this service actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg ServiceConfig
}

// ActorResolver names the principal responsible for an action, for the audit
// entry this package writes.
//
// It reads from the context because Submit's signature belongs to the subject,
// not to whoever is acting on their behalf — and those differ in exactly the
// case that matters. A support agent running an export for a customer is the
// event worth recording, and "who exported this person's data" is not
// answerable from the Subject alone.
//
// Without one, actions are attributed to audit.ActorSystem, which is honest
// for a self-service portal and misleading for a staff tool.
type ActorResolver func(ctx context.Context) audit.Actor

// encryptorPresent is a marker recording that artifacts are encrypted, without
// the Service holding an encryptor it would never use.
type encryptorPresent struct{}

func (encryptorPresent) Encrypt(context.Context, []byte, []byte) ([]byte, error) {
	return nil, platformerrors.New("dataprivacy service does not encrypt")
}

// NewService builds a Service.
//
// ops is where the work goes. It is a required argument rather than an option
// because a Service without one would record requests that nothing ever
// fulfills — which looks exactly like a working Service until a subject's
// statutory window runs out.
//
// It must be an operations Service whose registry has this package's kinds
// registered, which is what Fulfiller.Register does. That is true even on a
// process that only submits: operations.Service.Start resolves the kind at
// submission so that an unrunnable operation is refused there rather than
// discovered in a worker an hour later.
//
// ctx is used to validate the config and is not retained.
func NewService(
	ctx context.Context,
	cfg *ServiceConfig,
	store Store,
	ops operations.Service,
	opts ...ServiceOption,
) (*StoreService, error) {
	if cfg == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy service config")
	}

	if store == nil {
		return nil, ErrNilStore
	}

	if ops == nil {
		return nil, ErrNilOperations
	}

	cfg.EnsureDefaults()

	s := &StoreService{
		cfg:        *cfg,
		store:      store,
		operations: ops,
		clock:      clock.NewClock(),
		actor: func(context.Context) audit.Actor {
			return audit.Actor{ID: serviceName, Type: audit.ActorSystem}
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := s.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating dataprivacy service config")
	}

	s.o11y = observability.NewObserver(serviceName, s.logger, s.tracerProvider)

	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	var err error
	if s.submittedCounter, err = mp.NewInt64Counter(serviceName + "_requests_submitted"); err != nil {
		return nil, platformerrors.Wrap(err, "creating requests submitted counter")
	}
	if s.confirmedCounter, err = mp.NewInt64Counter(serviceName + "_requests_confirmed"); err != nil {
		return nil, platformerrors.Wrap(err, "creating requests confirmed counter")
	}
	if s.cancelledCounter, err = mp.NewInt64Counter(serviceName + "_requests_cancelled"); err != nil {
		return nil, platformerrors.Wrap(err, "creating requests cancelled counter")
	}
	if s.downloadCounter, err = mp.NewInt64Counter(serviceName + "_artifact_downloads"); err != nil {
		return nil, platformerrors.Wrap(err, "creating artifact downloads counter")
	}

	return s, nil
}

func (s *StoreService) Submit(ctx context.Context, subject Subject, t RequestType) (*Request, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		subjectIDKey:    subject.ID,
		subjectTypeKey:  string(subject.Type),
		subjectScopeKey: subject.Scope,
		requestTypeKey:  string(t),
	}))
	defer op.End()

	if err := subject.validate(); err != nil {
		return nil, op.Error(err, "validating dataprivacy subject")
	}

	if !t.Valid() {
		return nil, op.Error(platformerrors.Wrapf(ErrUnknownRequestType, "dataprivacy request type %q", t), "validating request type")
	}

	now := s.clock.Now().UTC()

	req := &Request{
		ID:          identifiers.New(),
		Type:        t,
		Subject:     subject,
		Status:      StatusInProgress,
		RequestedAt: now,
		DueAt:       now.Add(s.cfg.responseWindow(t)),
	}

	// Only erasure is ever held for confirmation. An export is reversible in
	// the only sense that matters — it can be expired and re-run — so making a
	// subject confirm one buys nothing and costs them a round trip.
	held := t == RequestErasure && s.cfg.ConfirmationWindow > 0
	if held {
		req.Status = StatusAwaitingConfirmation
		req.ExpiresAt = now.Add(s.cfg.ConfirmationWindow)
	}

	op.Set(requestIDKey, req.ID).Set(statusKey, string(req.Status))

	var started *operations.Operation

	if err := s.store.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		// Started inside the request's own transaction, so a process that dies
		// between the two leaves neither. The alternative — record the request,
		// commit, then start — produces a request nothing is fulfilling, which
		// is the shape of bug that is discovered by a subject rather than by a
		// deploy.
		if !held {
			var startErr error
			if started, startErr = s.start(ctx, q, req); startErr != nil {
				return startErr
			}

			req.OperationID = started.ID
		}

		if err := s.store.Save(ctx, q, req); err != nil {
			return err
		}

		return s.record(ctx, q, req, audit.EventCreated, nil)
	}); err != nil {
		return nil, op.Error(err, "submitting dataprivacy request")
	}

	s.enqueue(ctx, started)

	op.Set(operationIDKey, req.OperationID)
	s.submittedCounter.Add(ctx, 1, requestTypeAttr(t))

	return req, nil
}

// start records the operation that will fulfill a request, in the caller's
// transaction.
//
// The operation's request payload is a Job carrying the request ID and nothing
// else — see Job — and its owner is the subject, which is what lets
// operations/http scope a status read to the person it is about.
func (s *StoreService) start(
	ctx context.Context,
	q database.SQLQueryExecutor,
	req *Request,
) (*operations.Operation, error) {
	kind, ok := KindFor(req.Type)
	if !ok {
		return nil, platformerrors.Wrapf(ErrUnknownRequestType, "dataprivacy request type %q", req.Type)
	}

	started, err := s.operations.StartInTransaction(ctx, q, kind, Job{RequestID: req.ID},
		operations.WithOwner(req.Subject.ID))
	if err != nil {
		return nil, platformerrors.Wrapf(err, "starting the %s operation", kind)
	}

	return started, nil
}

// enqueue offers a started operation to the work queue, after the transaction
// that recorded it has committed.
//
// It cannot be inside that transaction — an enqueue is a batched upsert shared
// between callers and joins nobody's transaction — so it happens here, and a
// failure is logged rather than returned. The operation is durable either way:
// the operations recovery sweep picks up anything that was recorded and never
// queued, and returning an error would tell a subject their request was not
// accepted when the row says it was.
func (s *StoreService) enqueue(ctx context.Context, started *operations.Operation) {
	if started == nil {
		return
	}

	if err := s.operations.Enqueue(ctx, started.ID); err != nil {
		s.o11y.Logger().WithValue(operationIDKey, started.ID).
			Error("enqueuing a dataprivacy operation; it will be recovered by the operations sweep", err)
	}
}

func (s *StoreService) Get(ctx context.Context, requestID string) (*Request, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(requestIDKey, requestID))
	defer op.End()

	req, err := s.store.Get(ctx, requestID)
	if err != nil {
		return nil, op.Error(err, "reading dataprivacy request")
	}

	return req, nil
}

func (s *StoreService) List(
	ctx context.Context,
	subject Subject,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Request], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(subjectIDKey, subject.ID),
		observability.WithValue(subjectScopeKey, subject.Scope),
	)
	defer op.End()

	if err := subject.validate(); err != nil {
		return nil, op.Error(err, "validating dataprivacy subject")
	}

	results, err := s.store.List(ctx, subject, filter)
	if err != nil {
		return nil, op.Error(err, "listing dataprivacy requests")
	}

	return results, nil
}

func (s *StoreService) Confirm(ctx context.Context, requestID string) (*Request, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(requestIDKey, requestID))
	defer op.End()

	var (
		req     *Request
		started *operations.Operation
	)

	// The status change and the operation that acts on it commit together, for
	// the reason Submit does the same: a confirmation recorded without the work
	// it authorized is a request that will sit in progress until the statutory
	// window closes.
	if err := s.store.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		// Read first, because the operation has to be started with the request's
		// own type and subject and the transition does not know them. The guard
		// is still the transition's — this read decides nothing.
		existing, err := s.store.Get(ctx, requestID)
		if err != nil {
			return err
		}

		if started, err = s.start(ctx, q, existing); err != nil {
			return err
		}

		if req, err = s.store.Transition(ctx, q, requestID,
			[]Status{StatusAwaitingConfirmation}, StatusInProgress, started.ID, s.clock.Now().UTC()); err != nil {
			return err
		}

		return s.record(ctx, q, req, audit.EventUpdated, map[string]string{"reason": "confirmed"})
	}); err != nil {
		return nil, op.Error(s.notAwaitingConfirmation(requestID, err), "confirming dataprivacy request")
	}

	s.enqueue(ctx, started)

	op.Set(operationIDKey, req.OperationID)
	s.confirmedCounter.Add(ctx, 1, requestTypeAttr(req.Type))

	return req, nil
}

func (s *StoreService) Cancel(ctx context.Context, requestID string) (*Request, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(requestIDKey, requestID))
	defer op.End()

	existing, err := s.store.Get(ctx, requestID)
	if err != nil {
		return nil, op.Error(err, "reading dataprivacy request")
	}

	// A request already in progress is stopped through its operation rather than
	// through this row. The runner is the only thing that knows what a
	// half-finished fan-out has left behind, and it marks this row when it
	// stops — see Fulfiller and Service.Cancel's contract.
	if existing.Status == StatusInProgress {
		return s.cancelInFlight(ctx, op, existing)
	}

	var req *Request

	if err = s.store.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		var txErr error
		if req, txErr = s.store.Transition(ctx, q, requestID,
			[]Status{StatusAwaitingConfirmation}, StatusCancelled, "", s.clock.Now().UTC()); txErr != nil {
			return txErr
		}

		return s.record(ctx, q, req, audit.EventUpdated, map[string]string{"reason": "cancelled by request"})
	}); err != nil {
		return nil, op.Error(s.notAwaitingConfirmation(requestID, err), "cancelling dataprivacy request")
	}

	s.cancelledCounter.Add(ctx, 1, requestTypeAttr(req.Type))

	return req, nil
}

// cancelInFlight asks a running request's operation to stop.
//
// The row is left in StatusInProgress, and that is the honest reading: the work
// has not stopped yet. It stops when its runner next looks, at a point the
// runner can describe, and the runner is what moves this row — so a caller that
// wants to know whether it worked watches the operation, which is where a
// cancellation has always been a request rather than a fact.
func (s *StoreService) cancelInFlight(
	ctx context.Context,
	op observability.Operation,
	req *Request,
) (*Request, error) {
	op.Set(operationIDKey, req.OperationID)

	// A request in progress with no operation is the gap Submit's transaction
	// exists to close, so reaching it means somebody wrote the row by hand.
	// Reported rather than ignored: there is nothing to cancel and nothing
	// running, and saying "cancelled" would be false.
	if req.OperationID == "" {
		return nil, platformerrors.Wrapf(ErrNotAwaitingConfirmation,
			"dataprivacy request %q is in progress and names no operation", req.ID)
	}

	if _, err := s.operations.Cancel(ctx, req.OperationID); err != nil {
		return nil, platformerrors.Wrapf(err, "cancelling the operation fulfilling dataprivacy request %q", req.ID)
	}

	s.cancelledCounter.Add(ctx, 1, requestTypeAttr(req.Type))

	return req, nil
}

// notAwaitingConfirmation rewrites a guard miss into the answer the caller
// actually asked about.
//
// A transition that matched no row means the request was not awaiting
// confirmation — it was already confirmed, already cancelled, or its window
// lapsed while the subject was reading the mail. The caller is told which of
// those it is, not merely that something did not happen.
func (s *StoreService) notAwaitingConfirmation(requestID string, err error) error {
	if errors.Is(err, ErrRequestNotFound) {
		return platformerrors.Wrapf(ErrNotAwaitingConfirmation, "dataprivacy request %q", requestID)
	}

	return err
}

func (s *StoreService) Download(ctx context.Context, requestID string) (string, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(requestIDKey, requestID))
	defer op.End()

	req, err := s.artifactRequest(ctx, requestID)
	if err != nil {
		return "", op.Error(err, "resolving dataprivacy artifact")
	}

	if s.packager.encrypts() {
		return "", op.Error(
			platformerrors.Wrapf(ErrArtifactEncrypted, "dataprivacy request %q", requestID),
			"signing dataprivacy artifact URL",
		)
	}

	signer, ok := s.uploader.(uploads.URLSigner)
	if !ok {
		return "", op.Error(
			platformerrors.Wrapf(ErrNoURLSigner, "dataprivacy request %q", requestID),
			"signing dataprivacy artifact URL",
		)
	}

	url, err := signer.SignedURL(ctx, req.ArtifactRef, &uploads.SignedURLOptions{
		Method: "GET",
		Expiry: s.cfg.SignedURLTTL,
	})
	if err != nil {
		return "", op.Error(err, "signing dataprivacy artifact URL")
	}

	s.downloadCounter.Add(ctx, 1, requestTypeAttr(req.Type))

	// Recorded, and recorded outside the read's transaction because there is
	// not one. Minting a link to an export is the moment the data becomes
	// reachable, and it is the event an investigation asks about — more so than
	// the export that produced it, which at least required a subject to ask.
	if err = s.recordOutOfBand(ctx, req, audit.EventAccessed, map[string]string{"action": "download_url_issued"}); err != nil {
		op.Acknowledge(err, "recording dataprivacy artifact access")
	}

	return url, nil
}

func (s *StoreService) Open(ctx context.Context, requestID string) (io.ReadCloser, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(requestIDKey, requestID))
	defer op.End()

	req, err := s.artifactRequest(ctx, requestID)
	if err != nil {
		return nil, op.Error(err, "resolving dataprivacy artifact")
	}

	stored, err := uploads.ReadFile(ctx, s.uploader, req.ArtifactRef)
	if err != nil {
		return nil, op.Error(err, "reading dataprivacy artifact")
	}

	decoded, err := s.packager.decode(ctx, stored, req.ID)
	if err != nil {
		return nil, op.Error(err, "decoding dataprivacy artifact")
	}

	s.downloadCounter.Add(ctx, 1, requestTypeAttr(req.Type))

	if err = s.recordOutOfBand(ctx, req, audit.EventAccessed, map[string]string{"action": "artifact_read"}); err != nil {
		op.Acknowledge(err, "recording dataprivacy artifact access")
	}

	return io.NopCloser(bytes.NewReader(decoded)), nil
}

// artifactRequest resolves a request that actually has a fetchable artifact.
func (s *StoreService) artifactRequest(ctx context.Context, requestID string) (*Request, error) {
	if s.uploader == nil {
		return nil, platformerrors.Wrap(ErrArtifactUnavailable, "no dataprivacy upload manager configured")
	}

	req, err := s.store.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}

	// Status and reference are both checked. A completed export whose reference
	// is empty has been expired and swept; an expired one has too, and saying
	// so by status alone would miss the window between the object's deletion
	// and the row's update.
	if req.Status != StatusCompleted || req.ArtifactRef == "" {
		return nil, platformerrors.Wrapf(
			ErrArtifactUnavailable,
			"dataprivacy request %q is %s", requestID, req.Status,
		)
	}

	return req, nil
}

// record appends an audit entry inside the caller's transaction. It is a no-op
// without a Recorder.
func (s *StoreService) record(
	ctx context.Context,
	q database.SQLQueryExecutor,
	req *Request,
	event audit.EventType,
	metadata map[string]string,
) error {
	if s.recorder == nil {
		return nil
	}

	return s.recorder.Record(ctx, q, s.entryFor(ctx, req, event, metadata))
}

// recordOutOfBand appends an audit entry in a transaction of its own, for the
// reads that have none.
func (s *StoreService) recordOutOfBand(ctx context.Context, req *Request, event audit.EventType, metadata map[string]string) error {
	if s.recorder == nil {
		return nil
	}

	return s.store.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		return s.recorder.Record(ctx, q, s.entryFor(ctx, req, event, metadata))
	})
}

// entryFor renders one audit entry about a request.
//
// The subject's ID is recorded and nothing else about them is. An audit log is
// durable by design, and copying a person's data into the log that records the
// request to export it would defeat both.
func (s *StoreService) entryFor(ctx context.Context, req *Request, event audit.EventType, metadata map[string]string) *audit.Entry {
	fields := map[string]string{
		"request_type": string(req.Type),
		"status":       string(req.Status),
		"subject_id":   req.Subject.ID,
		"subject_type": string(req.Subject.Type),
	}
	maps.Copy(fields, metadata)

	return &audit.Entry{
		EventType:    event,
		ResourceType: auditResourceType,
		ResourceID:   req.ID,
		Actor:        s.actor(ctx),
		Scope:        req.Subject.Scope,
		Metadata:     fields,
		RecordedAt:   s.clock.Now().UTC(),
	}
}

// requestTypeAttr labels a measurement with its request type. There are exactly
// two, so this cannot grow cardinality, and without it the counters cannot
// distinguish a surge of exports from a surge of erasures — which need entirely
// different responses.
func requestTypeAttr(t RequestType) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(requestTypeKey, string(t)))
}
