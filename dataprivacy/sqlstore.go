package dataprivacy

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v14/dataprivacy/internal/dataprivacydb"
	"github.com/primandproper/platform-go/v14/dataprivacy/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlguard"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
)

// DefaultTablePrefix is the namespace the dataprivacy tables carry when none is
// configured, which is none — rendering dataprivacy_requests.
//
// The dataprivacy_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_dataprivacy_requests, for a database shared between applications. A namespace must
// not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans and logger. It is deliberately not
// serviceName: a trace showing the state machine and the rows it moved wants
// those distinguishable, and one scope for both would make a store read look
// like a Service call in every span listing.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema dataprivacy/migrations
// renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam every backing
// shares.
type SQLStore struct {
	client database.Client
	q      dataprivacydb.Querier
	o11y   observability.Observer

	guardMissCounter metrics.Int64Counter

	// guard is what a guarded write means in this package when it matches no
	// row. See internal/sqlguard.
	guard sqlguard.Guard

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	prefix          string
}

// NewSQLStore builds a Store over the given database.
//
// The dialect comes from the client, so the two cannot disagree. The prefix must
// still match the one the migrations were rendered with — nothing here can check
// that, and a mismatch surfaces as a missing table on the first query rather
// than at construction.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger, traces to a noop provider, and counts into a noop meter.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "dataprivacy dialect %q", d)
	}

	s := &SQLStore{
		client: client,
		prefix: DefaultTablePrefix,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := migrations.ValidatePrefix(s.prefix); err != nil {
		return nil, err
	}

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see dataprivacy/internal/queries.
	qd, err := dataprivacydbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := dataprivacydb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the dataprivacy querier")
	}

	s.q = q

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	// One counter, and only one. The Worker and Sweeper already own the business
	// totals — claimed, completed, failed, lapsed, reaped, expired — and a second
	// name for the same event is how two dashboards come to disagree. What no
	// caller can count is this: a guarded write that matched no row. That is not
	// a database error, and above this layer it is indistinguishable from one.
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	if s.guardMissCounter, err = mp.NewInt64Counter(storeName + "_guard_misses"); err != nil {
		return nil, platformerrors.Wrap(err, "creating dataprivacy store guard miss counter")
	}

	s.guard = sqlguard.Guard{
		MissCounter: s.guardMissCounter,
		NotFound:    ErrRequestNotFound,
		Namespace:   "dataprivacy",
		IDKey:       requestIDKey,
		Message:     "dataprivacy request left processing before its completion could be recorded",
		Reason:      "dataprivacy request %q is no longer being processed",
	}

	return s, nil
}

// dataprivacydbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewSQLStore has already rejected
// anything d.Valid() declines — so the default arm is reachable only when this
// module learns a dialect the generated package was not generated for. That is a
// construction failure like any other, and it names the dialect rather than
// panicking or leaning on dataprivacydb.New refusing the empty string.
func dataprivacydbDialect(d dialect.Dialect) (dataprivacydb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return dataprivacydb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return dataprivacydb.DialectMySQL, nil
	case dialect.SQLite:
		return dataprivacydb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated dataprivacy queries for dialect %q", d)
	}
}

// checkArtifactExpiry refuses a write that would record an artifact reference
// with no expiry.
//
// Save and CompleteExport are the only statements that write a non-empty
// artifact_ref — MarkExpired writes the empty one when the object is deleted,
// and nothing else touches the column — so guarding both is what makes "every
// artifact this table names is one a sweep will visit" a property of the store
// rather than a habit of its callers. The exported Store surface invites those
// callers, and the one who omitted the expiry is the one who has not thought
// about how long a person's data footprint should sit in a bucket.
func checkArtifactExpiry(req *Request) error {
	if req.ArtifactRef == "" || !req.ExpiresAt.IsZero() {
		return nil
	}

	return platformerrors.Wrapf(ErrUnexpiringArtifact,
		"dataprivacy request %q names artifact %q", req.ID, req.ArtifactRef)
}

func (s *SQLStore) Save(ctx context.Context, q database.Tx, req *Request) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return op.Error(ErrNilExecutor, "saving dataprivacy request")
	}

	if req == nil {
		return op.Error(ErrNilRequest, "saving dataprivacy request")
	}

	op.SetValues(map[string]any{
		requestIDKey:   req.ID,
		requestTypeKey: string(req.Type),
		statusKey:      string(req.Status),
		subjectIDKey:   req.Subject.ID,
	})

	if err := checkArtifactExpiry(req); err != nil {
		return op.Error(err, "saving dataprivacy request")
	}

	failures, retained, err := encodeMaps(req)
	if err != nil {
		return op.Error(err, "encoding dataprivacy request maps")
	}

	if err = s.q.CreateRequest(ctx, q, createRequestParams(req, failures, retained)); err != nil {
		return op.Error(err, "inserting dataprivacy request")
	}

	return nil
}

func (s *SQLStore) Get(ctx context.Context, requestID string) (*Request, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(requestIDKey, requestID))
	defer op.End()

	row, err := s.q.GetRequest(ctx, s.client.Reader(), dataprivacydb.GetRequestParams{ID: requestID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Attached to the span but not logged as an error. A request ID that
			// is not in the table is a 404 somebody is owed, or a record
			// retention has swept — neither is a fault of this process, and
			// painting the trace red for it buries the ones that are.
			op.Set(guardMissedKey, true)

			return nil, platformerrors.Wrapf(ErrRequestNotFound, "dataprivacy request %q", requestID)
		}

		return nil, op.Error(err, "reading dataprivacy request")
	}

	req, err := requestFromRow(&row)
	if err != nil {
		return nil, op.Error(err, "reading dataprivacy request")
	}

	op.Set(statusKey, string(req.Status)).Set(requestTypeKey, string(req.Type))

	return req, nil
}

func (s *SQLStore) List(
	ctx context.Context,
	subject Subject,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Request], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		subjectIDKey:    subject.ID,
		subjectTypeKey:  string(subject.Type),
		subjectScopeKey: subject.Scope,
	}))
	defer op.End()

	filter = pageFilter(filter)

	op.Set(limitKey, int(*filter.MaxResponseSize))

	rows, err := s.subjectPage(ctx, subject, filter)
	if err != nil {
		return nil, op.Error(err, "listing dataprivacy requests")
	}

	// The cursor is the id, because the statement orders by it. A cursor naming
	// a position in an order the query does not use is a page that skips rows
	// and repeats others, with nothing reporting an error.
	page := filtering.Drain(rows, pageValue, pageCounts,
		func(r *Request) string { return r.ID }, filter)

	// Read off the result rather than off the rows, so the span reports the
	// number the caller was handed. A page with no rows carries no counts — the
	// counts ride on the rows — and filtering.Drain reports that as unknown
	// rather than as zero.
	op.Set(resultCountKey, len(rows)).Set(resultTotalKey, page.TotalCount)

	return page, nil
}

// subjectPage runs whichever of the four list statements this call is: one scope
// or every scope, ascending or descending.
//
// The scope reading is a statement rather than a predicate that changes shape.
// An empty Subject.Scope means every scope the subject appears in — a subject
// asking what has been requested in their name means all of it, and a listing
// that quietly omitted the scoped requests would be the wrong answer to the one
// question this endpoint exists to answer — and there is no bound value that
// turns an equality into "any".
func (s *SQLStore) subjectPage(
	ctx context.Context,
	subject Subject,
	filter *filtering.QueryFilter,
) ([]pageRow, error) {
	if subject.Scope == "" {
		return s.anyScopePage(ctx, subject, filter)
	}

	params := listRequestsParams(subject, filter)

	if !filter.SortsDescending() {
		got, err := s.q.ListRequestsForSubject(ctx, s.client.Reader(), params)
		if err != nil {
			return nil, err
		}

		return pageRows(got, requestFromListRow)
	}

	got, err := s.q.ListRequestsForSubjectDescending(ctx, s.client.Reader(),
		dataprivacydb.ListRequestsForSubjectDescendingParams(params))
	if err != nil {
		return nil, err
	}

	// The descending rows are converted rather than restated field by field.
	// The two statements are one projection rendered twice, with the walk
	// reversed and nothing else changed, so the conversion is the assertion:
	// the day the projections stop being identical this stops building rather
	// than filling the wrong fields.
	ascending := make([]dataprivacydb.ListRequestsForSubjectRow, 0, len(got))
	for i := range got {
		ascending = append(ascending, dataprivacydb.ListRequestsForSubjectRow(got[i]))
	}

	return pageRows(ascending, requestFromListRow)
}

// anyScopePage is subjectPage's unscoped half.
func (s *SQLStore) anyScopePage(
	ctx context.Context,
	subject Subject,
	filter *filtering.QueryFilter,
) ([]pageRow, error) {
	params := listAnyScopeParams(subject, filter)

	if !filter.SortsDescending() {
		got, err := s.q.ListRequestsForSubjectInAnyScope(ctx, s.client.Reader(), params)
		if err != nil {
			return nil, err
		}

		return pageRows(got, requestFromAnyScopeRow)
	}

	got, err := s.q.ListRequestsForSubjectInAnyScopeDescending(ctx, s.client.Reader(),
		dataprivacydb.ListRequestsForSubjectInAnyScopeDescendingParams(params))
	if err != nil {
		return nil, err
	}

	ascending := make([]dataprivacydb.ListRequestsForSubjectInAnyScopeRow, 0, len(got))
	for i := range got {
		ascending = append(ascending, dataprivacydb.ListRequestsForSubjectInAnyScopeRow(got[i]))
	}

	return pageRows(ascending, requestFromAnyScopeRow)
}

// pageRows converts a statement's rows, stopping at the first that will not
// convert. A row that fails to decode is a row whose stored JSON is not a string
// map, and reporting the page without it would hand a subject a history missing
// the one request something is wrong with.
func pageRows[R any](rows []R, convert func(*R) (pageRow, error)) ([]pageRow, error) {
	converted := make([]pageRow, 0, len(rows))

	for i := range rows {
		row, err := convert(&rows[i])
		if err != nil {
			return nil, err
		}

		converted = append(converted, row)
	}

	return converted, nil
}

// pageFilter is the filter a paged read is answered under: the caller's, with
// the page-size ceiling every other paged read in this module applies.
//
// It works on a copy. The clamp has to be applied to what the query binds and to
// what the result reports, and doing that by writing through the caller's
// pointer would hand them back a filter they did not pass.
//
// A page size that is present and zero is left alone and returns no rows, which
// is the loud reading of an explicit zero. Only absence is defaulted.
func pageFilter(filter *filtering.QueryFilter) *filtering.QueryFilter {
	if filter == nil {
		return filtering.DefaultQueryFilter()
	}

	bounded := *filter

	size := uint16(filtering.DefaultQueryFilterLimit)
	if bounded.MaxResponseSize != nil {
		size = filtering.ClampResponseSize(uint64(*bounded.MaxResponseSize))
	}

	bounded.MaxResponseSize = &size

	return &bounded
}

func (s *SQLStore) Confirm(ctx context.Context, q database.Tx, requestID, operationID string) (*Request, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		requestIDKey:   requestID,
		statusKey:      string(StatusInProgress),
		operationIDKey: operationID,
	}))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "confirming dataprivacy request")
	}

	affected, err := s.q.ConfirmRequest(ctx, q, dataprivacydb.ConfirmRequestParams{
		Status:        string(StatusInProgress),
		OperationID:   operationID,
		ExpiresAt:     nil,
		ID:            requestID,
		CurrentStatus: string(StatusAwaitingConfirmation),
	})
	if err != nil {
		return nil, op.Error(err, "confirming dataprivacy request")
	}

	return s.movedRequest(ctx, op, q, requestID, affected, "confirm")
}

func (s *SQLStore) Cancel(
	ctx context.Context,
	q database.Tx,
	requestID string,
	from Status,
	at time.Time,
) (*Request, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValues(map[string]any{
		requestIDKey:  requestID,
		statusKey:     string(StatusCancelled),
		fromStatusKey: string(from),
	}))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "cancelling dataprivacy request")
	}

	if !from.Valid() {
		return nil, op.Error(platformerrors.Wrapf(ErrUnknownStatus, "dataprivacy status %q", from),
			"cancelling dataprivacy request")
	}

	completed := at.UTC()

	affected, err := s.q.CancelRequest(ctx, q, dataprivacydb.CancelRequestParams{
		Status:        string(StatusCancelled),
		CompletedAt:   &completed,
		ExpiresAt:     nil,
		ID:            requestID,
		CurrentStatus: string(from),
	})
	if err != nil {
		return nil, op.Error(err, "cancelling dataprivacy request")
	}

	return s.movedRequest(ctx, op, q, requestID, affected, "cancel")
}

// movedRequest reports a guarded transition that matched nothing and re-reads
// one that did.
//
// The re-read goes through the caller's executor rather than through the client,
// so the caller sees the row as its own transaction has it. Reading through the
// client here would go to the read replica and could return the pre-transition
// row.
func (s *SQLStore) movedRequest(
	ctx context.Context,
	op observability.Operation,
	q database.Tx,
	requestID string,
	affected int64,
	operation string,
) (*Request, error) {
	op.Set(rowsAffectedKey, affected)

	if affected == 0 {
		// The guard in the predicate did its job and nothing moved: the request
		// is gone, or a concurrent writer got there first — a subject clicking
		// confirm twice, or the lapse sweep cancelling as they clicked. Counted
		// rather than logged as an error, because from here it is not
		// distinguishable from ordinary contention, and it is the caller that
		// knows whether losing this particular race matters.
		op.Set(guardMissedKey, true)
		s.guardMissCounter.Add(ctx, 1, s.guard.OpAttr(operation))

		return nil, platformerrors.Wrapf(ErrRequestNotFound, "dataprivacy request %q in expected status", requestID)
	}

	row, err := s.q.GetRequest(ctx, q, dataprivacydb.GetRequestParams{ID: requestID})
	if err != nil {
		return nil, op.Error(err, "reading transitioned dataprivacy request")
	}

	req, err := requestFromRow(&row)
	if err != nil {
		return nil, op.Error(err, "reading transitioned dataprivacy request")
	}

	return req, nil
}

func (s *SQLStore) CompleteExport(ctx context.Context, q database.Tx, req *Request, at time.Time) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return op.Error(ErrNilExecutor, "completing dataprivacy export")
	}

	if req == nil {
		return op.Error(ErrNilRequest, "completing dataprivacy export")
	}

	op.SetValues(map[string]any{
		requestIDKey:    req.ID,
		artifactRefKey:  req.ArtifactRef,
		artifactSizeKey: req.ArtifactBytes,
		failureCountKey: len(req.Failures),
	})

	if err := checkArtifactExpiry(req); err != nil {
		return op.Error(err, "completing dataprivacy export")
	}

	failures, err := encodeMap(req.Failures)
	if err != nil {
		return op.Error(err, "encoding dataprivacy export failures")
	}

	completed := at.UTC()

	affected, err := s.q.CompleteExport(ctx, q, dataprivacydb.CompleteExportParams{
		Status:        string(StatusCompleted),
		CompletedAt:   &completed,
		ExpiresAt:     instant(req.ExpiresAt),
		ArtifactRef:   req.ArtifactRef,
		ArtifactBytes: req.ArtifactBytes,
		Failures:      failures,
		LastError:     new(""),
		ID:            req.ID,
		CurrentStatus: string(StatusInProgress),
	})
	return s.guard.Count(ctx, op, affected, err, req.ID, "export", "completing dataprivacy export")
}

// WithTransaction delegates to the client, which begins its own span for the
// transaction. Wrapping it here would nest a second span around the first and
// say nothing the client's does not.
func (s *SQLStore) WithTransaction(ctx context.Context, fn func(q database.Tx) error) error {
	return s.client.WithTransaction(ctx, fn)
}

func (s *SQLStore) CompleteErasure(ctx context.Context, q database.Tx, req *Request, at time.Time) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return op.Error(ErrNilExecutor, "completing dataprivacy erasure")
	}

	if req == nil {
		return op.Error(ErrNilRequest, "completing dataprivacy erasure")
	}

	op.SetValues(map[string]any{
		requestIDKey:    req.ID,
		deletedKey:      req.Deleted,
		anonymizedKey:   req.Anonymized,
		retainedKey:     len(req.Retained),
		failureCountKey: len(req.Failures),
	})

	failures, retained, err := encodeMaps(req)
	if err != nil {
		return op.Error(err, "encoding dataprivacy erasure maps")
	}

	completed := at.UTC()

	// expires_at is cleared rather than set. An erasure has no artifact to
	// expire, and the column held its confirmation window — leaving that behind
	// would have the lapse sweep cancel a request that has already run.
	affected, err := s.q.CompleteErasure(ctx, q, dataprivacydb.CompleteErasureParams{
		Status:         string(StatusCompleted),
		CompletedAt:    &completed,
		ExpiresAt:      nil,
		DeletedRows:    req.Deleted,
		AnonymizedRows: req.Anonymized,
		Failures:       failures,
		Retained:       retained,
		KeyShreddedAt:  utcPtr(req.KeyShreddedAt),
		LastError:      new(""),
		ID:             req.ID,
		CurrentStatus:  string(StatusInProgress),
	})
	return s.guard.Count(ctx, op, affected, err, req.ID, "erasure", "completing dataprivacy erasure")
}

func (s *SQLStore) MarkKeyShredded(ctx context.Context, requestID string, at time.Time) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(requestIDKey, requestID))
	defer op.End()

	shredded := at.UTC()

	affected, err := s.q.MarkKeyShredded(ctx, s.client.Writer(), dataprivacydb.MarkKeyShreddedParams{
		KeyShreddedAt: &shredded,
		ID:            requestID,
	})
	if err != nil {
		return op.Error(err, "recording dataprivacy key destruction")
	}

	// Zero rows is not a guard miss worth counting. It means a retry re-shredded
	// a key that was already destroyed and already recorded, which is the normal
	// shape of a retried erasure rather than a lost race.
	op.Set(rowsAffectedKey, affected)

	return nil
}

func (s *SQLStore) Fail(ctx context.Context, requestID, lastErr string, at time.Time) (bool, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(requestIDKey, requestID))
	defer op.End()

	completed := at.UTC()

	affected, err := s.q.FailRequest(ctx, s.client.Writer(), dataprivacydb.FailRequestParams{
		Status:        string(StatusFailed),
		LastError:     new(lastErr),
		CompletedAt:   &completed,
		ExpiresAt:     nil,
		ID:            requestID,
		CurrentStatus: string(StatusInProgress),
	})
	if err != nil {
		return false, op.Error(err, "recording dataprivacy request failure")
	}

	op.Set(rowsAffectedKey, affected)

	// Zero rows is reported rather than returned as an error. The request left
	// StatusInProgress before the final attempt gave up — cancelled, or
	// completed by a duplicate execution that got there first — and in both of
	// those the row already says something truer than "failed" would.
	if affected == 0 {
		op.Set(guardMissedKey, true)

		return false, nil
	}

	return true, nil
}

func (s *SQLStore) ExpiringArtifacts(ctx context.Context, now time.Time, limit int) ([]*Request, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer op.End()

	if limit <= 0 {
		return nil, nil
	}

	horizon := now.UTC()

	rows, err := s.q.ListExpiringArtifacts(ctx, s.client.Reader(), dataprivacydb.ListExpiringArtifactsParams{
		Status:        string(StatusCompleted),
		ExpiresBefore: &horizon,
		ResultLimit:   int64(limit),
	})
	if err != nil {
		return nil, op.Error(err, "selecting expiring dataprivacy artifacts")
	}

	requests := make([]*Request, 0, len(rows))
	for i := range rows {
		req, convErr := requestFromExpiringRow(&rows[i])
		if convErr != nil {
			return nil, op.Error(convErr, "selecting expiring dataprivacy artifacts")
		}

		requests = append(requests, req)
	}

	op.Set(resultCountKey, len(requests))

	// A sweep that keeps coming back full is a sweep that is not keeping up, and
	// the thing it is failing to delete is a file containing everything the
	// application knows about somebody.
	if len(requests) == limit {
		op.Logger().WithValue(limitKey, limit).
			Info("dataprivacy artifact expiry sweep filled its batch; artifacts may be expiring faster than they are swept")
	}

	return requests, nil
}

func (s *SQLStore) MarkExpired(ctx context.Context, requestID string, at time.Time) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(requestIDKey, requestID))
	defer op.End()

	expired := at.UTC()

	// The reference is cleared as the status changes, so a stale path cannot
	// outlive the object it named and be handed to a signer later.
	affected, err := s.q.ExpireArtifact(ctx, s.client.Writer(), dataprivacydb.ExpireArtifactParams{
		Status:        string(StatusExpired),
		ArtifactRef:   "",
		ExpiresAt:     &expired,
		ID:            requestID,
		CurrentStatus: string(StatusCompleted),
	})
	if err != nil {
		return op.Error(err, "expiring dataprivacy artifact")
	}

	// Zero rows is not an error and never was. The sweeper deletes the object
	// first and stamps afterwards, so a row that has moved on in between — a
	// second sweeper that got there first — has already recorded what this call
	// was going to say.
	op.Set(rowsAffectedKey, affected)

	return nil
}

func (s *SQLStore) LapseUnconfirmed(ctx context.Context, now time.Time, limit int) (int64, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer op.End()

	if limit <= 0 {
		return 0, nil
	}

	horizon := now.UTC()

	lapsed, err := s.q.LapseUnconfirmedRequests(ctx, s.client.Writer(), dataprivacydb.LapseUnconfirmedRequestsParams{
		Status:        string(StatusCancelled),
		CompletedAt:   &horizon,
		ExpiresAt:     nil,
		CurrentStatus: string(StatusAwaitingConfirmation),
		ExpiresBefore: &horizon,
		ResultLimit:   int64(limit),
	})
	if err != nil {
		return 0, op.Error(err, "lapsing unconfirmed dataprivacy erasures")
	}

	op.Set(lapsedKey, lapsed)

	return lapsed, nil
}

func (s *SQLStore) CountOverdue(ctx context.Context, now time.Time) (map[RequestType]int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	horizon := now.UTC()

	// Seeded with a zero for every type, and asked for one type at a time. Both
	// halves are the same decision: the gauge reports a number per request type
	// whether or not any request of that type is overdue, so a dashboard that
	// was reporting three overdue exports actively drops to zero when they are
	// served rather than holding a stale reading forever. A grouped count
	// answers only for the types that had rows, which is the reading that
	// leaves the stale number on the screen.
	counts := map[RequestType]int64{RequestExport: 0, RequestErasure: 0}

	for _, requestType := range []RequestType{RequestExport, RequestErasure} {
		row, err := s.q.CountOverdueRequests(ctx, s.client.Reader(), dataprivacydb.CountOverdueRequestsParams{
			RequestType: string(requestType),
			DueBefore:   horizon,
		})
		if err != nil {
			return nil, op.Error(err, "counting overdue dataprivacy requests")
		}

		counts[requestType] = row.Count
	}

	op.Set(overdueKey, counts[RequestExport]+counts[RequestErasure])

	return counts, nil
}

func (s *SQLStore) Reap(ctx context.Context, before time.Time, limit int) (int64, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer op.End()

	if limit <= 0 {
		return 0, nil
	}

	horizon := before.UTC()

	reaped, err := s.q.ReapRequests(ctx, s.client.Writer(), dataprivacydb.ReapRequestsParams{
		CompletedBefore: &horizon,
		ResultLimit:     int64(limit),
	})
	if err != nil {
		return 0, op.Error(err, "reaping dataprivacy requests")
	}

	op.Set(reapedKey, reaped)

	return reaped, nil
}
