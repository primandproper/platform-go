package dataprivacy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/cryptography/shredding"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/panicking"
	"github.com/primandproper/platform-go/v13/uploads"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// maxStoredErrorLength bounds a stored error rendering. A collector that
// returns a database error containing the row it choked on could otherwise
// write that row into the request record — which is to say, copy the subject's
// data into the table that records the request to delete it.
const maxStoredErrorLength = 1024

// Failure codes this package attaches to the operations it fails, for a client
// that has to branch on why rather than read a sentence.
const (
	// CodeRequestGone is the code for a runner whose request row is not there.
	// It means retention reaped it, or it never existed — an operation started
	// against an ID nothing wrote.
	CodeRequestGone = "dataprivacy_request_gone"

	// CodeNotInProgress is the code for a runner whose request row moved on
	// while the operation was queued: cancelled, or already fulfilled.
	CodeNotInProgress = "dataprivacy_request_not_in_progress"

	// CodeCancelled is the code a runner that stopped because it was asked to
	// records. The operation is recorded as cancelled whatever the runner
	// returns, so this is what an operator reads to see it exited at a point it
	// could describe rather than mid-write.
	CodeCancelled = "dataprivacy_cancelled"

	// CodeEverySectionFailed is the code for an export in which no collector
	// succeeded. See ErrEverySectionFailed.
	CodeEverySectionFailed = "dataprivacy_every_section_failed"

	// CodeDocumentTooLarge is the code for an assembled export past
	// FulfillerConfig.MaxDocumentBytes.
	CodeDocumentTooLarge = "dataprivacy_document_too_large"

	// CodeNoErasers is the code for an erasure with nothing registered to run.
	CodeNoErasers = "dataprivacy_no_erasers"

	// CodeUnknownRequestType is the code for a request row naming a type this
	// build does not implement.
	CodeUnknownRequestType = "dataprivacy_unknown_request_type"
)

var (
	// ErrNoUploadManager indicates an export Fulfiller with nowhere to write. It
	// is refused at construction: a fulfiller that collects eleven domains and
	// then discovers it has no storage has already done all the expensive work
	// and still fails.
	ErrNoUploadManager = platformerrors.New("no dataprivacy upload manager configured")

	// ErrDocumentTooLarge indicates an assembled export past MaxDocumentBytes.
	ErrDocumentTooLarge = platformerrors.New("dataprivacy export document exceeds configured maximum")

	// ErrEverySectionFailed indicates an export in which no collector succeeded.
	//
	// A partial export is delivered with a manifest naming the gaps, because
	// most of somebody's data plus an honest account of the rest is worth
	// having. An export with no data at all is not a partial export — it is a
	// file asserting that nothing is held about a person, which is the one
	// wrong answer this package could give.
	ErrEverySectionFailed = platformerrors.New("no dataprivacy collector succeeded")

	// ErrInvalidFragment indicates a Collector that returned something that is
	// not valid JSON. It is caught before assembly rather than at read time,
	// because a malformed fragment would otherwise produce an artifact that
	// cannot be parsed at all — turning one domain's bug into a total loss.
	ErrInvalidFragment = platformerrors.New("dataprivacy collector returned invalid JSON")

	// ErrCollectorPanicked indicates a Collector that panicked. It is that
	// section's failure rather than the operation's: a nil map access in one
	// domain should cost that domain's section, not the export.
	ErrCollectorPanicked = platformerrors.New("dataprivacy collector panicked")

	// ErrEraserPanicked indicates an Eraser that panicked. Unlike a collector
	// panic this aborts the whole erasure, because every eraser shares one
	// transaction and a panic mid-way through leaves no coherent partial state
	// to record.
	ErrEraserPanicked = platformerrors.New("dataprivacy eraser panicked")
)

// panicStackKey carries a contained panic's stack. Span-only: a stack trace is
// long, is attached to something already being reported as an error, and does
// not belong in every log aggregator's index.
const panicStackKey = "dataprivacy.panic_stack"

// shredRetentionKey is the Retained entry a skipped shred is recorded under.
//
// It cannot collide with an eraser's entry: those are namespaced as
// "<eraserKey>.<what>" and always contain a dot, and this deliberately does not.
const shredRetentionKey = "encryption_keys"

// Fulfiller does the work behind this package's two operation kinds.
//
// It is not a loop and owns no goroutine. That is the whole of what the port
// onto operations changed here: an operations Worker claims the work, leases it,
// retries it, and reports where it got to, and this supplies the two functions
// it calls. What used to be a poll interval, a batch size, a lease, an attempt
// counter, and a backoff schedule in this package is now one worker's, shared
// with every other long-running thing in the application.
//
// Register it into an operations.Registry and run an operations.Worker over the
// same registry:
//
//	f, err := dataprivacy.NewFulfiller(ctx, &dataprivacy.FulfillerConfig{}, store, registry,
//	    dataprivacy.WithFulfillerUploadManager(uploader),
//	)
//	// ...
//	if err = f.Register(operationsRegistry); err != nil {
//	    return err
//	}
type Fulfiller struct {
	store    Store
	registry *Registry
	clock    clock.Clock
	o11y     observability.Observer
	uploader uploads.UploadManager
	notifier Notifier
	recorder audit.Recorder
	actor    ActorResolver
	shredder shredding.Shredder
	signer   func(ctx context.Context, req *Request) (string, time.Time)

	packager packager

	completedCounter  metrics.Int64Counter
	failedCounter     metrics.Int64Counter
	sectionCounter    metrics.Int64Counter
	sectionErrCounter metrics.Int64Counter
	partialCounter    metrics.Int64Counter
	notifyErrCounter  metrics.Int64Counter
	cancelledCounter  metrics.Int64Counter
	erasedCounter     metrics.Int64Counter
	fulfillHist       metrics.Float64Histogram
	collectHist       metrics.Float64Histogram
	artifactHist      metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read f.o11y.Logger() for the logger this fulfiller actually uses; this
	// one may be nil, because supplying none is how a caller asks for no
	// logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg FulfillerConfig
}

// NewFulfiller builds a Fulfiller. It does not run anything; see Register.
//
// ctx is used to validate the config and is not retained.
//
// A registry with no collectors and no erasers is refused. So is an export
// capability with no storage. Both would produce a fulfiller that accepts work,
// does nothing useful, and reports success — and an erasure service that erases
// nothing while reporting success is the worst failure available here, because
// nobody goes looking for it.
func NewFulfiller(
	ctx context.Context,
	cfg *FulfillerConfig,
	store Store,
	registry *Registry,
	opts ...FulfillerOption,
) (*Fulfiller, error) {
	if cfg == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy fulfiller config")
	}

	if store == nil {
		return nil, ErrNilStore
	}

	if registry == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy registry")
	}

	cfg.EnsureDefaults()

	f := &Fulfiller{
		cfg:      *cfg,
		store:    store,
		registry: registry,
		clock:    clock.NewClock(),
		actor: func(context.Context) audit.Actor {
			return audit.Actor{ID: serviceName, Type: audit.ActorSystem}
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(f)
		}
	}

	if err := f.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating dataprivacy fulfiller config")
	}

	if len(registry.CollectorKeys()) == 0 && len(registry.EraserKeys()) == 0 {
		return nil, ErrNoCollectors
	}

	if len(registry.CollectorKeys()) > 0 && f.uploader == nil {
		return nil, ErrNoUploadManager
	}

	f.o11y = observability.NewObserver(serviceName, f.logger, f.tracerProvider)

	if err := f.buildInstruments(); err != nil {
		return nil, err
	}

	return f, nil
}

// Register adds this package's kinds to an operations registry: KindExport when
// there are collectors, KindErasure when there are erasers.
//
// Every process that starts one of these operations has to register it too, not
// only the processes that run them — operations.Service.Start resolves the kind
// through the registry so that an unrunnable operation is refused at submission
// rather than discovered in a worker an hour later. In practice that means the
// API process builds a Fulfiller as well, with the same registry and the same
// storage, and simply never runs an operations.Worker.
func (f *Fulfiller) Register(registry *operations.Registry) error {
	if registry == nil {
		return platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil operations registry")
	}

	if len(f.registry.CollectorKeys()) > 0 {
		if err := operations.Register(registry, operations.Definition[Job]{
			Kind: KindExport,
			// Bytes rather than records, because a Collector hands back one
			// opaque fragment per domain and there is nothing inside it this
			// package is entitled to count. Bytes is what it actually knows, it
			// is the thing MaxDocumentBytes bounds, and it moves once per domain
			// rather than once per row — coarse, and true.
			CountLabel:  "bytes",
			MaxAttempts: f.cfg.MaxAttempts,
			Run:         f.runExport,
		}); err != nil {
			return platformerrors.Wrap(err, "registering the dataprivacy export operation")
		}
	}

	if len(f.registry.EraserKeys()) > 0 {
		if err := operations.Register(registry, operations.Definition[Job]{
			Kind:        KindErasure,
			CountLabel:  "rows",
			MaxAttempts: f.cfg.MaxAttempts,
			Run:         f.runErasure,
		}); err != nil {
			return platformerrors.Wrap(err, "registering the dataprivacy erasure operation")
		}
	}

	return nil
}

// buildInstruments creates the Fulfiller's metrics up front, so a misconfigured
// meter fails the constructor rather than the first operation.
//
// The instruments the poll loop owned are gone with it: claims, leases, and
// batch sizes are the operations worker's to count now, and a second name for
// the same event is how two dashboards come to disagree.
func (f *Fulfiller) buildInstruments() error {
	mp := metrics.EnsureMetricsProvider(f.metricsProvider)

	var err error
	if f.completedCounter, err = mp.NewInt64Counter(serviceName + "_requests_completed"); err != nil {
		return platformerrors.Wrap(err, "creating requests completed counter")
	}
	if f.failedCounter, err = mp.NewInt64Counter(serviceName + "_requests_failed"); err != nil {
		return platformerrors.Wrap(err, "creating requests failed counter")
	}
	if f.cancelledCounter, err = mp.NewInt64Counter(serviceName + "_requests_stopped"); err != nil {
		return platformerrors.Wrap(err, "creating requests stopped counter")
	}
	if f.sectionCounter, err = mp.NewInt64Counter(serviceName + "_sections_collected"); err != nil {
		return platformerrors.Wrap(err, "creating sections collected counter")
	}
	if f.sectionErrCounter, err = mp.NewInt64Counter(serviceName + "_section_failures"); err != nil {
		return platformerrors.Wrap(err, "creating section failures counter")
	}
	if f.partialCounter, err = mp.NewInt64Counter(serviceName + "_exports_partial"); err != nil {
		return platformerrors.Wrap(err, "creating partial exports counter")
	}
	if f.notifyErrCounter, err = mp.NewInt64Counter(serviceName + "_notification_failures"); err != nil {
		return platformerrors.Wrap(err, "creating notification failures counter")
	}
	if f.erasedCounter, err = mp.NewInt64Counter(serviceName + "_rows_erased"); err != nil {
		return platformerrors.Wrap(err, "creating rows erased counter")
	}
	if f.fulfillHist, err = mp.NewFloat64Histogram(serviceName + "_fulfillment_latency_ms"); err != nil {
		return platformerrors.Wrap(err, "creating fulfillment latency histogram")
	}
	if f.collectHist, err = mp.NewFloat64Histogram(serviceName + "_collector_latency_ms"); err != nil {
		return platformerrors.Wrap(err, "creating collector latency histogram")
	}
	if f.artifactHist, err = mp.NewFloat64Histogram(serviceName + "_artifact_bytes"); err != nil {
		return platformerrors.Wrap(err, "creating artifact size histogram")
	}

	return nil
}

// runExport is the Runner behind KindExport.
func (f *Fulfiller) runExport(ctx context.Context, job Job, rep operations.Reporter) (*operations.Result, error) {
	return f.run(ctx, job, rep, RequestExport, f.export)
}

// runErasure is the Runner behind KindErasure.
func (f *Fulfiller) runErasure(ctx context.Context, job Job, rep operations.Reporter) (*operations.Result, error) {
	return f.run(ctx, job, rep, RequestErasure, f.erase)
}

// run is the shell both kinds share: resolve the request, bound it, do the work,
// and record what happened to it.
//
// The two kinds differ only in the fulfill function, and every part around it —
// the status guard, the replay check, the timeout, the failure recording, the
// notification — is identical and has to stay identical. Two copies of this
// drifted in the prior art, and the half that drifted was the failure path,
// which is the half nobody exercises.
func (f *Fulfiller) run(
	ctx context.Context,
	job Job,
	rep operations.Reporter,
	requestType RequestType,
	fulfill func(context.Context, *Request, operations.Reporter) (*operations.Result, error),
) (*operations.Result, error) {
	attempt := rep.Attempt()

	ctx, op := f.o11y.Begin(ctx, observability.WithValues(map[string]any{
		requestIDKey:   job.RequestID,
		requestTypeKey: string(requestType),
		operationIDKey: attempt.ID,
		finalKey:       attempt.Final,
	}))
	defer op.End()

	req, done, err := f.resolve(ctx, job, requestType)
	if err != nil {
		return nil, op.Error(err, "resolving dataprivacy request")
	}

	// Already fulfilled by an earlier attempt whose lease lapsed before it could
	// say so. The work is done and the row is the proof; doing it again would
	// re-run every collector against the subject's data to produce the same
	// bytes at the same key.
	if done != nil {
		op.Set(statusKey, string(req.Status))

		return done, nil
	}

	op.Set(subjectIDKey, req.Subject.ID)

	recordLatency := op.Time(ctx, f.clock, f.fulfillHist, requestTypeAttr(req.Type))

	// Bounded so that a collector or an eraser that hangs cannot hold the
	// operation open indefinitely. The operation's own lease is no longer the
	// bound it used to be — every progress flush extends it, which is exactly
	// what makes long work possible — so this is the only thing standing between
	// a wedged domain and an operation that never terminates.
	//
	// The bounded context is deliberately not reused for the bookkeeping below:
	// on a timeout it is already done, so every write made through it — the one
	// recording the failure included — would fail too.
	fulfillCtx, cancel := context.WithTimeout(ctx, f.cfg.FulfillmentTimeout)
	defer cancel()

	result, err := fulfill(fulfillCtx, req, rep)

	recordLatency()

	if err != nil {
		return nil, f.recordFailure(ctx, req, attempt, err)
	}

	f.completedCounter.Add(ctx, 1, requestTypeAttr(req.Type))

	if req.Partial() {
		f.partialCounter.Add(ctx, 1)
		op.Set(failureCountKey, len(req.Failures))
	}

	f.notify(ctx, req)

	return result, nil
}

// resolve reads the request the operation names and decides whether there is
// anything left to do.
//
// A non-nil Result means an earlier attempt already fulfilled it: the operation
// succeeds with that attempt's outcome rather than repeating the work. Every
// other state the row could be in is a hard failure, because none of them
// becomes StatusInProgress again by waiting.
func (f *Fulfiller) resolve(
	ctx context.Context,
	job Job,
	requestType RequestType,
) (*Request, *operations.Result, error) {
	if job.RequestID == "" {
		return nil, nil, operations.Unretryable(operations.Fail(CodeRequestGone,
			"dataprivacy operation names no request"))
	}

	req, err := f.store.Get(ctx, job.RequestID)
	if err != nil {
		if errors.Is(err, ErrRequestNotFound) {
			return nil, nil, operations.Unretryable(operations.WithCode(CodeRequestGone, err))
		}

		// Anything else is the database, which is the case retries are for.
		return nil, nil, err
	}

	// A row whose type does not match the kind that was started for it is a
	// wiring fault, not a transient one, and running an erasure under an export's
	// runner would be the most expensive way to discover it.
	if req.Type != requestType {
		return nil, nil, operations.Unretryable(operations.Fail(CodeUnknownRequestType,
			"dataprivacy request %q is a %s, started as a %s", req.ID, req.Type, requestType))
	}

	switch req.Status {
	case StatusInProgress:
		return req, nil, nil
	case StatusCompleted:
		return req, resultFor(req), nil
	case StatusAwaitingConfirmation, StatusFailed, StatusExpired, StatusCancelled:
		return nil, nil, operations.Unretryable(operations.WithCode(CodeNotInProgress,
			platformerrors.Wrapf(ErrNotInProgress, "dataprivacy request %q is %s", req.ID, req.Status)))
	default:
		return nil, nil, operations.Unretryable(operations.WithCode(CodeNotInProgress,
			platformerrors.Wrapf(ErrNotInProgress, "dataprivacy request %q has unknown status %q", req.ID, req.Status)))
	}
}

// resultFor renders a completed request's outcome from the row.
//
// It is the row rather than the run that both summary types are shaped around —
// see ExportSummary — so a replay reports exactly what the attempt that did the
// work reported, rather than a thinner version of it.
func resultFor(req *Request) *operations.Result {
	var (
		detail []byte
		err    error
	)

	if req.Type == RequestErasure {
		detail, err = json.Marshal(ErasureSummary{
			KeyShreddedAt: req.KeyShreddedAt,
			Retained:      req.Retained,
			Deleted:       req.Deleted,
			Anonymized:    req.Anonymized,
		})
	} else {
		generatedAt := req.RequestedAt
		if req.CompletedAt != nil {
			generatedAt = *req.CompletedAt
		}

		detail, err = json.Marshal(ExportSummary{
			GeneratedAt: generatedAt,
			Failures:    req.Failures,
			Bytes:       req.ArtifactBytes,
		})
	}

	// A summary that will not encode is dropped rather than failing an operation
	// whose work is finished and recorded. Result.Detail is a note; Result.URI
	// and the request row are the answer, and neither depends on this.
	if err != nil {
		detail = nil
	}

	return &operations.Result{URI: req.ArtifactRef, Detail: detail}
}

// export collects, packages, stores, and records one export.
func (f *Fulfiller) export(
	ctx context.Context,
	req *Request,
	rep operations.Reporter,
) (*operations.Result, error) {
	ctx, op := f.o11y.Begin(ctx)
	defer op.End()

	// Checked before the fan-out rather than only after it, because collection
	// is the expensive half: every registered domain runs a query against the
	// application's own database on behalf of a request somebody has withdrawn.
	if operations.Cancelled(rep) {
		return nil, f.stop(ctx, req, "stopped before collecting")
	}

	doc, err := f.collect(ctx, req, rep)
	if err != nil {
		return nil, err
	}

	// Checked again before anything is written. The point of stopping between
	// units is to stop at a place that can be described, and "collected but not
	// delivered" is describable in a way that "half an object in a bucket" is
	// not.
	if operations.Cancelled(rep) {
		return nil, f.stop(ctx, req, "stopped after collecting %d sections", len(doc.Data))
	}

	stored, err := f.packager.encode(ctx, doc, req.ID)
	if err != nil {
		return nil, err
	}

	if int64(len(stored)) > f.cfg.MaxDocumentBytes {
		return nil, operations.Unretryable(operations.WithCode(CodeDocumentTooLarge, platformerrors.Wrapf(
			ErrDocumentTooLarge,
			"dataprivacy export is %d bytes, limit is %d", len(stored), f.cfg.MaxDocumentBytes,
		)))
	}

	ref := artifactPath(f.cfg.ArtifactPathPrefix, req.ID)

	if err = f.uploader.Save(ctx, ref, bytes.NewReader(stored),
		uploads.WithContentType(f.packager.contentType()),
		// Explicitly uncacheable. The object is a person's entire data
		// footprint behind an expiring URL, and a cache between the bucket and
		// the subject would keep serving it after the link expired and after
		// the sweeper deleted it.
		uploads.WithCacheControl("private, no-store"),
	); err != nil {
		return nil, platformerrors.Wrap(err, "storing dataprivacy export artifact")
	}

	now := f.clock.Now().UTC()

	req.ArtifactRef = ref
	req.ArtifactBytes = int64(len(stored))
	req.ExpiresAt = now.Add(f.cfg.ArtifactTTL)
	req.Failures = doc.Manifest.Failures
	req.Status = StatusCompleted
	req.CompletedAt = &now

	f.artifactHist.Record(ctx, float64(len(stored)))

	// The reference and the size, not the contents. What is in the artifact is
	// the whole point of keeping it out of telemetry.
	op.Set(artifactRefKey, ref).Set(artifactSizeKey, req.ArtifactBytes)

	if err = f.store.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		if txErr := f.store.CompleteExport(ctx, q, req, now); txErr != nil {
			return txErr
		}

		return f.record(ctx, q, req, map[string]string{
			"artifact_bytes":  itoa(req.ArtifactBytes),
			"sections":        itoa(int64(len(doc.Data))),
			"failed_sections": itoa(int64(len(doc.Manifest.Failures))),
		})
	}); err != nil {
		// The object is written but the row does not say so. The sweeper only
		// deletes artifacts it can see a row for, so this leaves an orphan —
		// which is why the reference is derived from the request ID rather than
		// being random: a retry writes to the same key and overwrites it.
		return nil, platformerrors.Wrap(err, "recording completed dataprivacy export")
	}

	return resultFor(req), nil
}

// collect fans out over the registered collectors.
//
// Every collector gets its own timeout and its own error slot, and a failure is
// recorded against the key rather than propagated. That per-key isolation is
// the whole reason collection is a map of small interfaces instead of one
// method filling one shared struct: in the prior art a single domain returning
// an error aborted the aggregate, so a subject's export failed entirely because
// one unrelated table was slow.
//
// The domains are also the operation's unit tier, and they cost nothing to
// count: the registry already enumerates them, so "3 of 9 domains complete" is a
// SetUnits and a pair of calls rather than a counting pass. A failed section
// still finishes its unit — the export completes over all nine, and which of
// them came back empty is Result.Detail's business rather than the progress
// bar's.
func (f *Fulfiller) collect(ctx context.Context, req *Request, rep operations.Reporter) (*Document, error) {
	ctx, op := f.o11y.Begin(ctx)
	defer op.End()

	keys := f.registry.CollectorKeys()

	op.Set(requestIDKey, req.ID).Set(sectionCountKey, len(keys))

	rep.SetUnits(len(keys))

	var (
		mu       sync.Mutex
		data     = make(map[string]json.RawMessage, len(keys))
		failures = map[string]string{}
		sem      = make(chan struct{}, f.cfg.CollectorConcurrency)
		wg       sync.WaitGroup
	)

	for _, key := range keys {
		sem <- struct{}{}

		wg.Go(func() {
			defer func() { <-sem }()

			// Several of these are open at once, which the reporter counts
			// correctly and renders approximately: the numerator is exact and
			// Progress.Unit names whichever domain started most recently.
			rep.StartUnit(key)
			defer rep.FinishUnit()

			fragment, err := f.collectOne(ctx, key, req.Subject)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				failures[key] = truncateError(err)
				f.sectionErrCounter.Add(ctx, 1, sectionAttr(key))
				f.o11y.Logger().WithValues(map[string]any{
					requestIDKey: req.ID,
					sectionKey:   key,
				}).Error("collecting dataprivacy export section", err)

				return
			}

			// A nil fragment is "nothing about this subject", which is a
			// complete answer and not an empty one. Omitting the section says
			// that; writing null would claim the domain holds a null.
			if len(fragment) > 0 {
				data[key] = fragment
				rep.Advance(int64(len(fragment)))
				f.sectionCounter.Add(ctx, 1, sectionAttr(key))
			}
		})
	}

	wg.Wait()

	if len(data) == 0 && len(failures) > 0 {
		return nil, operations.WithCode(CodeEverySectionFailed,
			platformerrors.Wrapf(ErrEverySectionFailed, "%d of %d sections failed", len(failures), len(keys)))
	}

	if len(failures) == 0 {
		failures = nil
	}

	return &Document{
		Data: data,
		Manifest: Manifest{
			Format:      DocumentFormat,
			RequestID:   req.ID,
			Subject:     req.Subject,
			GeneratedAt: f.clock.Now().UTC(),
			Sections:    sortedKeys(data),
			Failures:    failures,
		},
	}, nil
}

// collectOne runs a single collector under its own timeout and span.
//
// A collector that panics is converted into that section's failure rather than
// taking the operation down. It is somebody else's code running in our
// goroutine, and a nil map access in one domain should cost that domain's
// section.
func (f *Fulfiller) collectOne(ctx context.Context, key string, subject Subject) (json.RawMessage, error) {
	ctx, op := f.o11y.Begin(ctx,
		observability.WithValue(sectionKey, key),
		observability.WithValue(subjectIDKey, subject.ID),
	)
	defer op.End()

	collector, ok := f.registry.Collector(key)
	if !ok {
		return nil, platformerrors.Newf("no dataprivacy collector registered for %q", key)
	}

	ctx, cancel := context.WithTimeout(ctx, f.cfg.CollectorTimeout)
	defer cancel()

	var fragment json.RawMessage

	recordLatency := op.Time(ctx, f.clock, f.collectHist, sectionAttr(key))

	err := panicking.Contain(func() error {
		var collectErr error
		fragment, collectErr = collector.Collect(ctx, subject)

		return collectErr
	})

	recordLatency()

	if err != nil {
		return nil, op.Error(containedPanic(op, err, ErrCollectorPanicked), "collecting dataprivacy section")
	}

	// Validated here rather than trusted, because one domain returning
	// malformed bytes would otherwise make the whole artifact unparseable —
	// converting one domain's bug into a total loss for the subject.
	if len(fragment) > 0 && !json.Valid(fragment) {
		return nil, op.Error(platformerrors.Wrapf(ErrInvalidFragment, "section %q", key), "validating collected fragment")
	}

	return fragment, nil
}

// erase destroys the subject's data key, runs every registered eraser, and
// records the outcome — the erasers and the bookkeeping in one transaction, the
// shred outside it and first.
//
// Erasure across domains is atomic, and deliberately so. A partial erasure has
// no coherent meaning: a subject who is deleted from eight domains and present
// in three has not been erased, and cannot be told they have. The alternative —
// per-domain isolation, as collection uses — would leave the system in a state
// no status could describe. So an eraser's error aborts the whole thing and the
// operation is retried intact.
//
// # Cancellation stops here or nowhere
//
// The check is before the shred and therefore before the transaction, and there
// is not a second one inside the loop. Both halves are irreversible in different
// ways: the shred cannot be undone at all, and abandoning the transaction
// half-way rolls back work that a retry would only redo. The last instant at
// which stopping means anything is before either has begun, so that is where
// stopping is offered.
//
// # Why the shred is not in that transaction, and why it goes first
//
// It cannot be in it. The keys are meant to live in a database of their own, on
// a shorter backup retention than the data they protect — that separation is
// what stops a restore resurrecting a shredded key — and there is no
// transaction spanning two databases here.
//
// Given that it is separate, it goes first, because the two failure modes are
// not equally bad. Shred-then-fail leaves ciphertext nobody can read and rows a
// retry will delete: the subject's data is already beyond recovery, which is the
// direction an erasure request wants to fail in. Erase-then-fail-to-shred leaves
// the live rows gone and every backup still readable, for as long as it takes
// the retry to succeed — the exact gap this feature exists to close, reopened at
// the worst moment.
//
// The cost is real and worth stating: a request that shreds and then exhausts
// its attempts has destroyed the key permanently while leaving rows behind.
// Request.KeyShreddedAt records that this happened, and the request is marked
// failed, so it is visible rather than merely true. Two-phase confirmation is
// what keeps a mistaken request from getting this far — by the time an operation
// runs it, somebody has confirmed it.
//
// It also means an Eraser must not depend on reading the subject's encrypted
// columns. By the time it runs they are noise.
func (f *Fulfiller) erase(
	ctx context.Context,
	req *Request,
	rep operations.Reporter,
) (*operations.Result, error) {
	ctx, op := f.o11y.Begin(ctx)
	defer op.End()

	keys := f.registry.EraserKeys()

	op.Set(requestIDKey, req.ID).Set(sectionCountKey, len(keys))

	if len(keys) == 0 {
		return nil, operations.Unretryable(operations.WithCode(CodeNoErasers, ErrNoErasers))
	}

	rep.SetUnits(len(keys))

	if operations.Cancelled(rep) {
		return nil, f.stop(ctx, req, "stopped before erasing")
	}

	now := f.clock.Now().UTC()

	shredNote, err := f.shred(ctx, req)
	if err != nil {
		return nil, err
	}

	err = f.store.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		var (
			deleted    int64
			anonymized int64
			// Seeded with whatever the shred had to say for itself, which is
			// nothing in the ordinary case and a stated basis when a scoped
			// request meant the key could not be destroyed. Cloned rather than
			// used directly so an eraser's entry cannot write into it.
			retained = maps.Clone(shredNote)
		)

		// Serially, not concurrently. Every eraser shares one transaction, and
		// a *sql.Tx is a single connection: concurrent statements on it are a
		// data race the driver will either serialize or reject.
		for _, key := range keys {
			// The progress flush this triggers writes to the operations table
			// rather than through q, so it does not join this transaction — and
			// it is what extends the operation's lease, which is how an erasure
			// across forty domains stays owned by the worker running it.
			//
			// It does need a connection of its own, though, and that is the one
			// deployment constraint this design imposes: a runner holds a
			// transaction for the whole of its work while a flush writes beside
			// it, so a connection pool with no spare capacity deadlocks rather
			// than merely slowing down. Size the pool for the worker's
			// concurrency plus its flushes, not for its concurrency.
			rep.StartUnit(key)

			outcome, eraseErr := f.eraseOne(ctx, q, key, req.Subject)
			if eraseErr != nil {
				return eraseErr
			}

			deleted += outcome.Deleted
			anonymized += outcome.Anonymized

			rep.Advance(outcome.Deleted + outcome.Anonymized)
			rep.FinishUnit()

			for what, basis := range outcome.Retained {
				// Namespaced by eraser key so two domains retaining "invoices"
				// for different reasons do not overwrite each other.
				retained[key+"."+what] = basis
			}
		}

		req.Deleted = deleted
		req.Anonymized = anonymized
		req.Status = StatusCompleted
		req.CompletedAt = &now

		if len(retained) > 0 {
			req.Retained = retained
		}

		if txErr := f.store.CompleteErasure(ctx, q, req, now); txErr != nil {
			return txErr
		}

		metadata := map[string]string{
			"deleted":    itoa(deleted),
			"anonymized": itoa(anonymized),
			"retained":   itoa(int64(len(retained))),
		}

		// The one fact in this entry that cannot be established any other way
		// afterwards. Deleted rows can be counted from what is missing; a
		// destroyed key leaves no trace in the data it protected, because that
		// is what destroying it did.
		if req.KeyShreddedAt != nil {
			metadata["key_shredded_at"] = req.KeyShreddedAt.Format(time.RFC3339Nano)
		}

		return f.record(ctx, q, req, metadata)
	})
	if err != nil {
		return nil, err
	}

	op.SetValues(map[string]any{
		deletedKey:    req.Deleted,
		anonymizedKey: req.Anonymized,
		retainedKey:   len(req.Retained),
	})

	f.erasedCounter.Add(ctx, req.Deleted+req.Anonymized)

	return resultFor(req), nil
}

// shred destroys the subject's data key, and reports anything the erasure
// record should say about why it did not.
//
// A scoped request is the case that cannot be served. Scope confines an erasure
// to one tenant, and a data key spans every scope its subject appears in — so
// destroying it would erase that person's data inside tenants nobody asked
// about. Skipping the shred is the only correct answer, and saying so in
// Retained is what keeps it from being a silent downgrade: the field already
// exists to record what an erasure kept and on what basis, and this is exactly
// that.
//
// The consequence is worth being plain about. A scoped erasure deletes rows and
// does not reach backups, which is the pre-shredding guarantee. An application
// that needs scoped requests to reach backups needs its keys scoped the same
// way, which is a decision about what a Subject is and has to be made before
// anything is encrypted.
func (f *Fulfiller) shred(ctx context.Context, req *Request) (map[string]string, error) {
	// Non-nil throughout, so the caller merges one map rather than branching on
	// whether there was anything to say.
	retained := map[string]string{}

	if f.shredder == nil {
		return retained, nil
	}

	ctx, op := f.o11y.Begin(ctx,
		observability.WithValue(requestIDKey, req.ID),
		observability.WithValue(subjectIDKey, req.Subject.ID),
		observability.WithValue(subjectScopeKey, req.Subject.Scope),
	)
	defer op.End()

	if req.Subject.Scope != "" {
		op.Set(shreddedKey, false)

		retained[shredRetentionKey] = "encryption keys retained: this request is confined to one scope, " +
			"and a subject's data key covers every scope they appear in, so destroying it would erase " +
			"data outside the request; rows in scope were deleted or anonymized as recorded"

		return retained, nil
	}

	receipt, err := f.shredder.Shred(ctx, shredding.Subject{
		Type: string(req.Subject.Type),
		ID:   req.Subject.ID,
	})
	if err != nil {
		return nil, op.Error(err, "shredding dataprivacy subject key")
	}

	req.KeyShreddedAt = &receipt.ShreddedAt

	// Recorded now rather than at completion. The key is gone whatever happens
	// to the rest of this request, including if it exhausts its attempts and
	// fails — and that outcome, a subject whose ciphertext is noise while their
	// rows remain, is exactly the one somebody needs to be able to find.
	if err = f.store.MarkKeyShredded(ctx, req.ID, receipt.ShreddedAt); err != nil {
		return nil, op.Error(err, "recording dataprivacy key destruction")
	}

	op.Set(shreddedKey, true).Set(keyDestroyedKey, receipt.Destroyed)

	return retained, nil
}

// eraseOne runs a single eraser, converting a panic into an error so that one
// domain's bug aborts the transaction cleanly rather than unwinding the runner
// with a half-applied erasure in flight.
func (f *Fulfiller) eraseOne(
	ctx context.Context,
	q database.SQLQueryExecutor,
	key string,
	subject Subject,
) (ErasureOutcome, error) {
	ctx, op := f.o11y.Begin(ctx,
		observability.WithValue(sectionKey, key),
		observability.WithValue(subjectIDKey, subject.ID),
	)
	defer op.End()

	eraser, ok := f.registry.Eraser(key)
	if !ok {
		return ErasureOutcome{}, platformerrors.Newf("no dataprivacy eraser registered for %q", key)
	}

	var outcome ErasureOutcome

	err := panicking.Contain(func() error {
		var eraseErr error
		outcome, eraseErr = eraser.Erase(ctx, q, subject)

		return eraseErr
	})
	if err != nil {
		return ErasureOutcome{}, op.Error(containedPanic(op, err, ErrEraserPanicked), "erasing dataprivacy section %q", key)
	}

	op.Set(deletedKey, outcome.Deleted).Set(anonymizedKey, outcome.Anonymized)

	return outcome, nil
}

// containedPanic turns a panic that panicking.Contain caught into one of this
// package's sentinels, putting the stack on the span first — the wrapped
// sentinel no longer carries it. Anything that is not a contained panic is
// returned untouched.
func containedPanic(op observability.Operation, err, sentinel error) error {
	pe, ok := errors.AsType[*panicking.PanicError](err)
	if !ok {
		return err
	}

	op.SpanOnly(panicStackKey, string(pe.Stack))

	return platformerrors.Wrapf(sentinel, "%v", pe.Value)
}

// stop records a request the runner abandoned because it was asked to, and
// renders the error it returns.
//
// The operation is recorded as cancelled whatever comes back from here — the
// worker decides that from the reporter, not from the error — so the error's job
// is only to say where the runner had got to. The row is moved separately,
// because nothing in operations knows this row exists.
func (f *Fulfiller) stop(ctx context.Context, req *Request, format string, args ...any) error {
	f.cancelledCounter.Add(ctx, 1, requestTypeAttr(req.Type))

	now := f.clock.Now().UTC()

	if err := f.store.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		stopped, txErr := f.store.Transition(ctx, q, req.ID,
			[]Status{StatusInProgress}, StatusCancelled, "", now)
		if txErr != nil {
			return txErr
		}

		return f.record(ctx, q, stopped, map[string]string{"reason": "cancelled while in progress"})
	}); err != nil {
		// Logged rather than returned. The operation is going to be recorded as
		// cancelled either way, and replacing that with a database error would
		// tell the client the work failed when what happened is that it stopped.
		f.o11y.Logger().WithValue(requestIDKey, req.ID).
			Error("marking a cancelled dataprivacy request", err)
	}

	return operations.Unretryable(operations.Fail(CodeCancelled, format, args...))
}

// recordFailure marks a request that will not be fulfilled and tells whoever was
// waiting, then hands the cause back for the operation to record.
//
// It writes nothing on an attempt that is not the last one, and that is the
// whole of the change from a poll loop that owned its own attempt counter. A row
// marked failed while the operation is going to try again would be read by the
// overdue gauge, by the sweeper, and by the subject's status page as a request
// that had been given up on — three wrong answers, twice, before the retry that
// succeeds.
func (f *Fulfiller) recordFailure(ctx context.Context, req *Request, attempt operations.Attempt, cause error) error {
	f.failedCounter.Add(ctx, 1, requestTypeAttr(req.Type))

	logger := f.o11y.Logger().WithValues(map[string]any{
		requestIDKey:   req.ID,
		requestTypeKey: string(req.Type),
		subjectIDKey:   req.Subject.ID,
		operationIDKey: attempt.ID,
		finalKey:       attempt.Final,
	})

	if !attempt.Final && !operations.IsUnretryable(cause) {
		logger.Info("dataprivacy request failed, the operation will retry")

		return cause
	}

	now := f.clock.Now().UTC()

	logger.Error("dataprivacy request failed permanently", cause)

	failed, err := f.store.Fail(ctx, req.ID, truncateError(cause), now)
	if err != nil {
		logger.Error("recording dataprivacy request failure", err)

		return cause
	}

	// The row moved on without us: cancelled, or completed by a duplicate
	// execution. Nothing to record and nobody to tell, because what the row
	// says is truer than what this attempt has to report.
	if !failed {
		return cause
	}

	req.Status = StatusFailed
	req.LastError = truncateError(cause)
	req.CompletedAt = &now

	// Somebody is owed an answer and is not going to get one. Telling them it
	// failed is better than a status page that says "in progress" until the
	// statutory window runs out — and this is the only moment at which that is
	// a true thing to say, which is why operations.Attempt exists.
	f.notify(ctx, req)

	return cause
}

// notify tells the subject the request is done. A notification failure is
// logged and counted but never fails the operation: the export exists and the
// erasure ran, and re-running either to retry an email would re-run every
// collector against the subject's data.
func (f *Fulfiller) notify(ctx context.Context, req *Request) {
	if f.notifier == nil {
		return
	}

	notification := &Notification{Request: req}

	if req.Status == StatusCompleted && req.Type == RequestExport && f.signer != nil {
		notification.DownloadURL, notification.ExpiresAt = f.signer(ctx, req)
	}

	if err := f.notifier.Notify(ctx, notification); err != nil {
		f.notifyErrCounter.Add(ctx, 1, requestTypeAttr(req.Type))
		f.o11y.Logger().WithValue(requestIDKey, req.ID).Error("notifying dataprivacy request subject", err)
	}
}

// record appends the completion audit entry inside the caller's transaction.
func (f *Fulfiller) record(
	ctx context.Context,
	q database.SQLQueryExecutor,
	req *Request,
	metadata map[string]string,
) error {
	if f.recorder == nil {
		return nil
	}

	fields := map[string]string{
		"request_type": string(req.Type),
		"status":       string(req.Status),
		"subject_id":   req.Subject.ID,
		"subject_type": string(req.Subject.Type),
	}
	maps.Copy(fields, metadata)

	return f.recorder.Record(ctx, q, &audit.Entry{
		EventType:    audit.EventUpdated,
		ResourceType: auditResourceType,
		ResourceID:   req.ID,
		Actor:        f.actor(ctx),
		Scope:        req.Subject.Scope,
		Metadata:     fields,
		RecordedAt:   f.clock.Now().UTC(),
	})
}

// artifactPath renders where an export is stored.
//
// The request ID is the filename rather than the subject's, deliberately.
// Object keys leak: they appear in access logs, in bucket listings, in URLs
// somebody pastes into a ticket. A path built from a subject identifier would
// make the storage layout itself a record of who has asked to be forgotten.
//
// It is also derived rather than random, so a retry after a failed completion
// overwrites the orphaned object instead of leaving one behind on every
// attempt.
func artifactPath(prefix, requestID string) string {
	return path.Join(prefix, requestID+".json")
}

// NewArtifactURLSigner builds the signer a Fulfiller hands to
// WithFulfillerURLSigner, so a completion notification can carry a working
// download link.
//
// It exists because the Fulfiller cannot hold a Service — a Service is the thing
// that reads what the Fulfiller writes — but the notification is only useful
// with a URL in it. The manager and TTL must be the ones the Service would use,
// or the subject gets a link into the wrong bucket.
//
// It declines to sign in exactly the cases Service.Download refuses: an
// encrypted artifact, and a provider that cannot sign. An empty URL is not an
// error here — the notification simply tells the subject their export is ready
// and to sign in for it, which is the correct message when a link cannot be
// handed out.
func NewArtifactURLSigner(
	manager uploads.UploadManager,
	ttl time.Duration,
	encrypted bool,
	opts ...URLSignerOption,
) func(ctx context.Context, req *Request) (string, time.Time) {
	signer, canSign := manager.(uploads.URLSigner)

	// The expiry it returns is the one the subject reads out of the notification
	// — "this link works until" — so it is stamped through a clock like every
	// other timestamp this package produces. Without one, a Fulfiller under a
	// test clock sent a notification whose deadline was in wall-clock time, and
	// the mismatch was only visible to whoever compared the two.
	var c clock.Clock = clock.NewClock()
	for _, opt := range opts {
		if opt != nil {
			opt(&c)
		}
	}

	return func(ctx context.Context, req *Request) (string, time.Time) {
		if !canSign || encrypted || req.ArtifactRef == "" {
			return "", time.Time{}
		}

		url, err := signer.SignedURL(ctx, req.ArtifactRef, &uploads.SignedURLOptions{
			Method: http.MethodGet,
			Expiry: ttl,
		})
		if err != nil {
			return "", time.Time{}
		}

		return url, c.Now().UTC().Add(ttl)
	}
}

// sectionAttr labels a measurement with its section. Cardinality is bounded by
// the registry, which is a fixed list written at wiring time.
func sectionAttr(key string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(sectionKey, key))
}

// truncateError renders an error for storage, bounded.
func truncateError(err error) string {
	return platformerrors.TruncateError(err, maxStoredErrorLength)
}

// itoa renders an int64 for an audit entry's metadata, which is a string map.
func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
