package dataprivacy

import (
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v14/dataprivacy/internal/dataprivacydb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The typed seam between the generated package and this package's own types.
//
// dataprivacy/internal/dataprivacydb is sqlc-gen-unison's output: one params
// and one row struct per statement, the same on all three dialects. These
// functions are the whole of what this package does with them — a row becomes a
// Request, a Request becomes the params — and every one is a struct literal on
// purpose. A renamed or retyped column changes the generated struct, and every
// conversion here stops compiling; the scan-by-position pairing these replaced
// reported the same mistake as a runtime scan error, or, where two columns
// shared a type, as two values silently transposed.
//
// The row structs are nominal per statement, so a list row cannot convert to a
// get row even where the columns agree. The five projections of this table are
// identical, and they are still restated one function each: restating is the
// cost, and the compiler checking every field name is what it buys.

// requestFromRow turns the unscoped single-request read into a Request.
func requestFromRow(r *dataprivacydb.GetRequestRow) (*Request, error) {
	return request(getRowFields(r))
}

// getRowFields is the unscoped single-request read's projection, unpacked.
//
// It is separate from requestFromRow because SQLStore.Get chooses its statement
// before it decodes anything: the two reads return nominally distinct row types
// carrying identical columns, and unpacking each into the shared fields is what
// lets one decode serve both.
func getRowFields(r *dataprivacydb.GetRequestRow) *requestFields {
	return &requestFields{
		id:             r.ID,
		requestType:    r.RequestType,
		status:         r.Status,
		operationID:    r.OperationID,
		subjectID:      r.SubjectID,
		subjectType:    r.SubjectType,
		subjectScope:   r.SubjectScope,
		createdAt:      r.CreatedAt,
		dueAt:          r.DueAt,
		expiresAt:      r.ExpiresAt,
		completedAt:    r.CompletedAt,
		artifactRef:    r.ArtifactRef,
		artifactBytes:  r.ArtifactBytes,
		deletedRows:    r.DeletedRows,
		anonymizedRows: r.AnonymizedRows,
		failures:       r.Failures,
		retained:       r.Retained,
		lastError:      r.LastError,
		keyShreddedAt:  r.KeyShreddedAt,
	}
}

// scopedGetRowFields is getRowFields for the read that names a confinement.
//
// The two projections are identical and this is still restated field by field,
// for the reason the file header gives: the row structs are nominal per
// statement, and the compiler checking every field name is what a restatement
// buys.
func scopedGetRowFields(r *dataprivacydb.GetRequestInScopeRow) *requestFields {
	return &requestFields{
		id:             r.ID,
		requestType:    r.RequestType,
		status:         r.Status,
		operationID:    r.OperationID,
		subjectID:      r.SubjectID,
		subjectType:    r.SubjectType,
		subjectScope:   r.SubjectScope,
		createdAt:      r.CreatedAt,
		dueAt:          r.DueAt,
		expiresAt:      r.ExpiresAt,
		completedAt:    r.CompletedAt,
		artifactRef:    r.ArtifactRef,
		artifactBytes:  r.ArtifactBytes,
		deletedRows:    r.DeletedRows,
		anonymizedRows: r.AnonymizedRows,
		failures:       r.Failures,
		retained:       r.Retained,
		lastError:      r.LastError,
		keyShreddedAt:  r.KeyShreddedAt,
	}
}

// requestFromExpiringRow turns one row of the artifact expiry sweep into a
// Request.
func requestFromExpiringRow(r *dataprivacydb.ListExpiringArtifactsRow) (*Request, error) {
	return request(&requestFields{
		id:             r.ID,
		requestType:    r.RequestType,
		status:         r.Status,
		operationID:    r.OperationID,
		subjectID:      r.SubjectID,
		subjectType:    r.SubjectType,
		subjectScope:   r.SubjectScope,
		createdAt:      r.CreatedAt,
		dueAt:          r.DueAt,
		expiresAt:      r.ExpiresAt,
		completedAt:    r.CompletedAt,
		artifactRef:    r.ArtifactRef,
		artifactBytes:  r.ArtifactBytes,
		deletedRows:    r.DeletedRows,
		anonymizedRows: r.AnonymizedRows,
		failures:       r.Failures,
		retained:       r.Retained,
		lastError:      r.LastError,
		keyShreddedAt:  r.KeyShreddedAt,
	})
}

// requestFromListRow turns one row of a subject's scoped history into a
// Request, with the counts the statement carried beside it.
func requestFromListRow(r *dataprivacydb.ListRequestsForSubjectRow) (pageRow, error) {
	req, err := request(&requestFields{
		id:             r.ID,
		requestType:    r.RequestType,
		status:         r.Status,
		operationID:    r.OperationID,
		subjectID:      r.SubjectID,
		subjectType:    r.SubjectType,
		subjectScope:   r.SubjectScope,
		createdAt:      r.CreatedAt,
		dueAt:          r.DueAt,
		expiresAt:      r.ExpiresAt,
		completedAt:    r.CompletedAt,
		artifactRef:    r.ArtifactRef,
		artifactBytes:  r.ArtifactBytes,
		deletedRows:    r.DeletedRows,
		anonymizedRows: r.AnonymizedRows,
		failures:       r.Failures,
		retained:       r.Retained,
		lastError:      r.LastError,
		keyShreddedAt:  r.KeyShreddedAt,
	})

	return pageRow{value: req, filtered: r.FilteredCount, total: r.TotalCount}, err
}

// requestFromAnyScopeRow is requestFromListRow for the statement that does not
// key on a scope.
func requestFromAnyScopeRow(r *dataprivacydb.ListRequestsForSubjectInAnyScopeRow) (pageRow, error) {
	req, err := request(&requestFields{
		id:             r.ID,
		requestType:    r.RequestType,
		status:         r.Status,
		operationID:    r.OperationID,
		subjectID:      r.SubjectID,
		subjectType:    r.SubjectType,
		subjectScope:   r.SubjectScope,
		createdAt:      r.CreatedAt,
		dueAt:          r.DueAt,
		expiresAt:      r.ExpiresAt,
		completedAt:    r.CompletedAt,
		artifactRef:    r.ArtifactRef,
		artifactBytes:  r.ArtifactBytes,
		deletedRows:    r.DeletedRows,
		anonymizedRows: r.AnonymizedRows,
		failures:       r.Failures,
		retained:       r.Retained,
		lastError:      r.LastError,
		keyShreddedAt:  r.KeyShreddedAt,
	})

	return pageRow{value: req, filtered: r.FilteredCount, total: r.TotalCount}, err
}

// requestFields is one row's columns, named as the table names them.
//
// It is the one place the column-to-field mapping is written out, and the
// conversions above are each a restatement of one nominal row struct into it.
// The row structs are per statement and the mapping is per table, so without
// this the same twenty assignments would appear once per projection and a
// mistake in one of them would be a mistake in one read.
type requestFields struct {
	createdAt      time.Time
	dueAt          time.Time
	expiresAt      *time.Time
	completedAt    *time.Time
	lastError      *string
	keyShreddedAt  *time.Time
	id             string
	requestType    string
	status         string
	operationID    string
	subjectID      string
	subjectType    string
	subjectScope   string
	artifactRef    string
	failures       []byte
	retained       []byte
	artifactBytes  int64
	deletedRows    int64
	anonymizedRows int64
}

// request assembles the domain value, decoding the two stored maps.
//
// Every timestamp is normalized to UTC, because every one this package writes
// is: Postgres hands a value back in the session's zone, MySQL in the server's
// and SQLite whatever the text parsed as, so a caller comparing two of them, or
// rendering one into JSON, would otherwise get an answer that depends on where
// the row was read.
//
// ExpiresAt is a value rather than a pointer on Request, so an absent one is the
// zero time — which is what the field has always meant for an erasure that was
// never held for confirmation.
func request(f *requestFields) (*Request, error) {
	req := &Request{
		ID:          f.id,
		Type:        RequestType(f.requestType),
		Status:      Status(f.status),
		OperationID: f.operationID,
		Subject: Subject{
			ID:   f.subjectID,
			Type: SubjectType(f.subjectType),
		},
		Scope:         subjectScope(f.subjectScope),
		CreatedAt:     f.createdAt.UTC(),
		DueAt:         f.dueAt.UTC(),
		ExpiresAt:     utcValue(f.expiresAt),
		CompletedAt:   utcPtr(f.completedAt),
		KeyShreddedAt: utcPtr(f.keyShreddedAt),
		ArtifactRef:   f.artifactRef,
		ArtifactBytes: f.artifactBytes,
		Deleted:       f.deletedRows,
		Anonymized:    f.anonymizedRows,
		LastError:     stringValue(f.lastError),
	}

	var err error
	if req.Failures, err = decodeMap(f.failures); err != nil {
		return nil, platformerrors.Wrap(err, "decoding dataprivacy request failures")
	}

	if req.Retained, err = decodeMap(f.retained); err != nil {
		return nil, platformerrors.Wrap(err, "decoding dataprivacy request retentions")
	}

	return req, nil
}

// createRequestParams renders a new request as the insert's arguments.
//
// created_at is among them, which is this table's one departure from the
// module's convention — see queries.InsertColumns. It is the instant the
// statutory window starts running and DueAt is that instant plus the window, so
// both ends of the deadline come from one clock.
func createRequestParams(req *Request, failures, retained []byte) dataprivacydb.CreateRequestParams {
	return dataprivacydb.CreateRequestParams{
		ID:             req.ID,
		CreatedAt:      req.CreatedAt.UTC(),
		RequestType:    string(req.Type),
		Status:         string(req.Status),
		OperationID:    req.OperationID,
		SubjectID:      req.Subject.ID,
		SubjectType:    string(req.Subject.Type),
		SubjectScope:   subjectScopeValue(req.Scope),
		DueAt:          req.DueAt.UTC(),
		ExpiresAt:      instant(req.ExpiresAt),
		CompletedAt:    utcPtr(req.CompletedAt),
		ArtifactRef:    req.ArtifactRef,
		ArtifactBytes:  req.ArtifactBytes,
		DeletedRows:    req.Deleted,
		AnonymizedRows: req.Anonymized,
		Failures:       failures,
		Retained:       retained,
		LastError:      new(req.LastError),
		KeyShreddedAt:  utcPtr(req.KeyShreddedAt),
	}
}

// listWindow is the filter window every generated list statement binds, in the
// shape the generated params carry it. One reading of the filter, restated into
// each nominal params type by the callers below.
type listWindow struct {
	createdAfter    *time.Time
	createdBefore   *time.Time
	updatedAfter    *time.Time
	updatedBefore   *time.Time
	pageCursor      *string
	resultLimit     int64
	includeArchived bool
}

// windowFrom reads the window off a page filter. The filter has been through
// pageFilter, so MaxResponseSize is set; only IncludeArchived defaults here, and
// it defaults to excluding, which is what the statement's COALESCE would have
// done with a NULL anyway — bound explicitly so the parameter is a bool rather
// than a pointer whose nil means the same thing.
func windowFrom(filter *filtering.QueryFilter) listWindow {
	w := listWindow{
		createdAfter:  utcPtr(filter.CreatedAfter),
		createdBefore: utcPtr(filter.CreatedBefore),
		updatedAfter:  utcPtr(filter.UpdatedAfter),
		updatedBefore: utcPtr(filter.UpdatedBefore),
		pageCursor:    filter.Cursor,
		resultLimit:   int64(*filter.MaxResponseSize),
	}

	if filter.IncludeArchived != nil {
		w.includeArchived = *filter.IncludeArchived
	}

	return w
}

// listRequestsParams binds a subject's history inside one confinement.
func listRequestsParams(
	scope tenancy.Scope,
	subject Subject,
	filter *filtering.QueryFilter,
) dataprivacydb.ListRequestsForSubjectParams {
	w := windowFrom(filter)

	return dataprivacydb.ListRequestsForSubjectParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		SubjectID:       subject.ID,
		SubjectScope:    subjectScopeValue(scope),
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

// listAnyScopeParams binds the same history across every scope the subject
// appears in.
func listAnyScopeParams(subject Subject, filter *filtering.QueryFilter) dataprivacydb.ListRequestsForSubjectInAnyScopeParams {
	w := windowFrom(filter)

	return dataprivacydb.ListRequestsForSubjectInAnyScopeParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		SubjectID:       subject.ID,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

// pageRow is one row of a rendered list query: the request, and the two counts
// the statement carries beside it.
//
// The counts ride on the rows rather than arriving from a second query, which is
// what makes a page and the number describing it come from one snapshot of the
// table. It also means a page with no rows carries no counts — see
// filtering.Drain, which reports that as unknown rather than as zero.
type pageRow struct {
	value    *Request
	filtered int64
	total    int64
}

func pageValue(row pageRow) *Request { return row.value }

func pageCounts(row pageRow) (filtered, total int64) { return row.filtered, row.total }

// utcPtr normalizes an optional timestamp to UTC, preserving absence.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// utcValue reads an optional timestamp as a value, an absent one as the zero
// time.
func utcValue(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}

	return t.UTC()
}

// instant renders a time this package holds as a value into the optional
// argument the column takes.
//
// The zero time becomes NULL rather than year 1. A zero timestamp stored as a
// value reads back as a moment long past, which every horizon comparison in the
// sweeps would treat as overdue — so an erasure with no confirmation window
// would be lapsed by the first sweep that saw it.
//
// The export side reads the same NULL the other way round, and is protected by
// a guard rather than by a second encoding. The artifact sweep matches only rows
// whose expires_at is set, so there a NULL means never swept rather than no
// deadline — and never swept, for a row that names an artifact, means a person's
// entire data footprint kept forever. checkArtifactExpiry refuses that write, so
// nothing carrying an artifact reference reaches this with a zero expiry.
func instant(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	return utcPtr(&t)
}

// stringValue reads a nullable text column, an absent one as the empty string.
//
// Nothing here writes a NULL into one: last_error is bound as the string it
// holds, empty included, so the two spellings mean the same thing on the way out
// while the way in has only one. The NULLs that reach this are the rows written
// before there was anything to say.
func stringValue(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// encodeMaps renders both of a request's string maps for storage.
func encodeMaps(req *Request) (failures, retained []byte, err error) {
	if failures, err = encodeMap(req.Failures); err != nil {
		return nil, nil, err
	}

	if retained, err = encodeMap(req.Retained); err != nil {
		return nil, nil, err
	}

	return failures, retained, nil
}

// encodeMap renders a string map for storage, or nil for an empty one. Nil and
// empty collapse deliberately: they say the same thing, and storing two
// renderings would make a round trip depend on which call site wrote the row.
func encodeMap(m map[string]string) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}

	encoded, err := json.Marshal(m)
	if err != nil {
		return nil, platformerrors.Wrap(err, "encoding dataprivacy request map")
	}

	return encoded, nil
}

// decodeMap reads a stored string map back, leaving an absent one nil.
//
// A nil map with a nil error is the intended result for a NULL column, not a
// missing value: "no failures" and "no retentions" are the common case, and a
// sentinel here would make every read branch on an error that means nothing went
// wrong.
func decodeMap(b []byte) (m map[string]string, err error) {
	if len(b) == 0 {
		return nil, nil //nolint:nilnil // an absent map is the normal reading, not an error
	}

	if err = json.Unmarshal(b, &m); err != nil {
		return nil, err
	}

	return m, nil
}

// subjectScope reads a stored confinement back: the empty identifier is the
// request that named no scope, and anything else is the scope it named.
//
// It is a conversion here rather than a tenancy.Scope on the generated
// parameter, which is what dataprivacy/unison.yaml would buy and deliberately
// does not. The column holds an optional confinement, and tenancy.Scope's own
// binding refuses the scope that names nobody — right for a column that must
// hold an owner, wrong for one whose emptiness is an answer. The scope that
// cannot survive this round trip is tenancy.Global, and validateScope refuses
// it rather than letting it arrive here.
func subjectScope(stored string) tenancy.Scope {
	if stored == "" {
		return tenancy.Scope{}
	}

	return tenancy.Of(stored)
}

// subjectScopeValue renders a confinement for the column, mapping the request
// that named no scope to the empty identifier. It is subjectScope's inverse.
func subjectScopeValue(scope tenancy.Scope) string {
	return scope.Owner()
}
