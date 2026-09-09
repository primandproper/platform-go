package issuereports

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/issuereports/internal/issuereportsdb"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/tenancy"
)

// CreateReport files one report through the caller's transaction and reads back
// the creation time the database assigned.
//
// The read-back is a second round trip on a write path, and it is worth it:
// created_at is database-owned — see issuereports/internal/queries — so the
// insert does not carry it, and the alternative is a value whose CreatedAt says
// 0001-01-01 for a row written a moment ago. A service that serializes what it
// just created straight into a response would render that as a date rather than
// as an absence.
//
// Every check and both statements run on tx, so the value handed back is the row
// this transaction wrote rather than a read of a row nothing else can see yet.
// See [Store.CreateReport].
func (s *SQLStore) CreateReport(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	report *Report,
) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "creating issue report")
	}

	if report == nil {
		return op.Error(ErrNilReport, "creating issue report")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "creating issue report")
	}

	if err := adoptScope(scope, report); err != nil {
		return op.Error(err, "creating issue report")
	}

	op.Set(reporterKey, report.Reporter)

	if err := validReport(report); err != nil {
		return op.Error(err, "creating issue report")
	}

	// A report is born open, and the caller does not get to say otherwise. A
	// value arriving resolved is a transition spelled as a create — the one move
	// that would skip the guard the rest of the lifecycle rests on — so it is
	// refused rather than silently corrected.
	switch report.Status {
	case "":
		report.Status = StatusOpen
	case StatusOpen:
	default:
		return op.Error(platformerrors.Wrapf(ErrInvalidStatusTransition,
			"an issue report is created %q, not %q", StatusOpen, report.Status),
			"creating issue report")
	}

	// Neither belongs to a report nobody has closed, and a caller that filled
	// them in is a caller who has a resolution for something still open.
	report.ClosedAt = nil
	report.Resolution = ""

	if report.ID == "" {
		report.ID = identifiers.New()
	}

	op.Set(reportIDKey, report.ID).Set(statusKey, report.Status.String())

	if err := s.q.CreateReport(ctx, tx, createReportParams(scope, report)); err != nil {
		return op.Error(err, "creating issue report")
	}

	created, err := s.q.GetReportCreatedAt(ctx, tx,
		issuereportsdb.GetReportCreatedAtParams{ID: report.ID, Scope: scope})
	if err != nil {
		return op.Error(err, "reading back the issue report's creation time")
	}

	report.CreatedAt = created.CreatedAt.UTC()

	return nil
}

// adoptScope settles which tenant a write is for, and writes the answer back
// onto the report.
//
// The scope the call named is the one the statement binds, so a report that
// names a different one is refused rather than corrected: the two disagreeing is
// a caller holding one tenant's report and writing it into another, which is a
// stale value or a mix-up and is not a thing to guess at. A report that names
// none adopts the argument, which is what keeps a caller assembling a fresh
// report from spelling the scope twice. tenancy.Scope tells its zero value apart
// from Global(), so "unset" here is genuinely unset rather than the global scope
// spelled shortly.
func adoptScope(scope tenancy.Scope, report *Report) error {
	if report.Scope != (tenancy.Scope{}) && report.Scope != scope {
		return platformerrors.Wrapf(ErrScopeMismatch,
			"issue report names %q, the write names %q", report.Scope, scope)
	}

	report.Scope = scope

	return nil
}

// GetReport reads one of the scope's live reports, on the caller's executor — so
// a caller inside a transaction reads the report that transaction has written
// and not yet committed.
func (s *SQLStore) GetReport(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	reportID string,
) (*Report, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(reportIDKey, reportID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading issue report %q", reportID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading issue report %q", reportID)
	}

	report, err := s.reportOn(ctx, q, scope, reportID)
	if err != nil {
		return nil, op.Error(err, "reading issue report %q", reportID)
	}

	return report, nil
}

// reportOn reads one live report through the executor it is given.
//
// It exists because a transition's two reads must go where the transition wrote:
// a row that transaction has written and not committed is visible on no other
// connection, so the report handed back would say it never moved, and the
// disambiguation of a missed guard would report a report that is there as
// absent.
func (s *SQLStore) reportOn(
	ctx context.Context,
	exec issuereportsdb.DBTX,
	scope tenancy.Scope,
	reportID string,
) (*Report, error) {
	row, err := s.q.GetReport(ctx, exec, issuereportsdb.GetReportParams{ID: reportID, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrReportNotFound)
	}

	return reportFromRow(&row), nil
}

// ListReports pages the scope's reports, in the direction the filter names.
func (s *SQLStore) ListReports(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Report], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing issue reports")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing issue reports")
	}

	filter = pageFilter(filter)

	rows, err := sortedRows(filter,
		func() ([]issuereportsdb.ListReportsRow, error) {
			return s.q.ListReports(ctx, q, listReportsParams(scope, filter))
		},
		func() ([]issuereportsdb.ListReportsDescendingRow, error) {
			return s.q.ListReportsDescending(ctx, q,
				issuereportsdb.ListReportsDescendingParams(listReportsParams(scope, filter)))
		},
		func(r issuereportsdb.ListReportsDescendingRow) issuereportsdb.ListReportsRow {
			return issuereportsdb.ListReportsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing issue reports")
	}

	return listPage(op, rows, filter), nil
}

// ListReportsByStatus is ListReports restricted to one status: the triage queue.
func (s *SQLStore) ListReportsByStatus(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	status Status,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Report], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(statusKey, status.String()),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing issue reports by status")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing issue reports by status")
	}

	// Refused rather than answered with an empty page. A queue that has been
	// quietly misspelled looks exactly like a queue nobody has filed into, and
	// the console showing it has no way to tell the difference.
	if !status.Valid() {
		return nil, op.Error(platformerrors.Wrapf(ErrUnknownStatus, "issue report status %q", status),
			"listing issue reports by status")
	}

	filter = pageFilter(filter)

	rows, err := sortedRows(filter,
		func() ([]issuereportsdb.ListReportsByStatusRow, error) {
			return s.q.ListReportsByStatus(ctx, q,
				listByStatusParams(scope, status, filter))
		},
		func() ([]issuereportsdb.ListReportsByStatusDescendingRow, error) {
			return s.q.ListReportsByStatusDescending(ctx, q,
				issuereportsdb.ListReportsByStatusDescendingParams(
					listByStatusParams(scope, status, filter)))
		},
		func(r issuereportsdb.ListReportsByStatusDescendingRow) issuereportsdb.ListReportsByStatusRow {
			return issuereportsdb.ListReportsByStatusRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing issue reports by status")
	}

	return listPage(op, convert(rows, func(r issuereportsdb.ListReportsByStatusRow) issuereportsdb.ListReportsRow {
		return issuereportsdb.ListReportsRow(r)
	}), filter), nil
}

// ListReportsByReporter pages the reports one person filed within the scope.
func (s *SQLStore) ListReportsByReporter(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	reporter string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Report], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(reporterKey, reporter),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing issue reports by reporter")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing issue reports by reporter")
	}

	if reporter == "" {
		return nil, op.Error(ErrEmptyReporter, "listing issue reports by reporter")
	}

	filter = pageFilter(filter)

	rows, err := sortedRows(filter,
		func() ([]issuereportsdb.ListReportsByReporterRow, error) {
			return s.q.ListReportsByReporter(ctx, q,
				listByReporterParams(scope, reporter, filter))
		},
		func() ([]issuereportsdb.ListReportsByReporterDescendingRow, error) {
			return s.q.ListReportsByReporterDescending(ctx, q,
				issuereportsdb.ListReportsByReporterDescendingParams(
					listByReporterParams(scope, reporter, filter)))
		},
		func(r issuereportsdb.ListReportsByReporterDescendingRow) issuereportsdb.ListReportsByReporterRow {
			return issuereportsdb.ListReportsByReporterRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing issue reports by reporter")
	}

	return listPage(op, convert(rows, func(r issuereportsdb.ListReportsByReporterRow) issuereportsdb.ListReportsRow {
		return issuereportsdb.ListReportsRow(r)
	}), filter), nil
}

// ListReportsBySubjectType pages every report about one kind of thing.
func (s *SQLStore) ListReportsBySubjectType(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subjectType string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Report], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectTypeKey, subjectType),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing issue reports by subject type")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing issue reports by subject type")
	}

	filter = pageFilter(filter)

	rows, err := sortedRows(filter,
		func() ([]issuereportsdb.ListReportsBySubjectTypeRow, error) {
			return s.q.ListReportsBySubjectType(ctx, q,
				listBySubjectTypeParams(scope, subjectType, filter))
		},
		func() ([]issuereportsdb.ListReportsBySubjectTypeDescendingRow, error) {
			return s.q.ListReportsBySubjectTypeDescending(ctx, q,
				issuereportsdb.ListReportsBySubjectTypeDescendingParams(
					listBySubjectTypeParams(scope, subjectType, filter)))
		},
		func(r issuereportsdb.ListReportsBySubjectTypeDescendingRow) issuereportsdb.ListReportsBySubjectTypeRow {
			return issuereportsdb.ListReportsBySubjectTypeRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing issue reports by subject type")
	}

	return listPage(op, convert(rows, func(r issuereportsdb.ListReportsBySubjectTypeRow) issuereportsdb.ListReportsRow {
		return issuereportsdb.ListReportsRow(r)
	}), filter), nil
}

// ListReportsForSubject pages every report about one particular thing.
func (s *SQLStore) ListReportsForSubject(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subjectType, subjectID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Report], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectTypeKey, subjectType),
		observability.WithValue(subjectIDKey, subjectID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing issue reports for subject")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing issue reports for subject")
	}

	filter = pageFilter(filter)

	rows, err := sortedRows(filter,
		func() ([]issuereportsdb.ListReportsForSubjectRow, error) {
			return s.q.ListReportsForSubject(ctx, q,
				listForSubjectParams(scope, subjectType, subjectID, filter))
		},
		func() ([]issuereportsdb.ListReportsForSubjectDescendingRow, error) {
			return s.q.ListReportsForSubjectDescending(ctx, q,
				issuereportsdb.ListReportsForSubjectDescendingParams(
					listForSubjectParams(scope, subjectType, subjectID, filter)))
		},
		func(r issuereportsdb.ListReportsForSubjectDescendingRow) issuereportsdb.ListReportsForSubjectRow {
			return issuereportsdb.ListReportsForSubjectRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing issue reports for subject")
	}

	return listPage(op, convert(rows, func(r issuereportsdb.ListReportsForSubjectRow) issuereportsdb.ListReportsRow {
		return issuereportsdb.ListReportsRow(r)
	}), filter), nil
}

// convert casts a narrowed list's rows to the base list's row type.
//
// The five list statements are one projection rendered five times, with more
// predicates each time and nothing else changed, so the conversion is the
// assertion: the day two of those projections stop being identical, in field
// name, type or order, this stops building rather than filling the wrong fields.
func convert[From, To any](rows []From, same func(From) To) []To {
	converted := make([]To, 0, len(rows))
	for i := range rows {
		converted = append(converted, same(rows[i]))
	}

	return converted
}

// listPage is the one place a page of reports becomes a result.
//
// The cursor is the id, because every list statement orders by it. A cursor
// naming a position in an order the query does not use is a page that skips rows
// and repeats others, with nothing reporting an error — so the five lists share
// this rather than each naming the field they page by.
func listPage(
	op observability.Operation,
	listRows []issuereportsdb.ListReportsRow,
	filter *filtering.QueryFilter,
) *filtering.QueryFilteredResult[Report] {
	rows := make([]pageRow, 0, len(listRows))
	for i := range listRows {
		rows = append(rows, reportPageRow(&listRows[i]))
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(r *Report) string { return r.ID }, filter)
}

// UpdateReport revises what the reporter said, through the caller's transaction,
// so the revision and whatever the caller records about it commit together or
// not at all. See [Store.UpdateReport].
func (s *SQLStore) UpdateReport(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	report *Report,
) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "updating issue report")
	}

	if report == nil {
		return op.Error(ErrNilReport, "updating issue report")
	}

	op.Set(reportIDKey, report.ID)

	if err := scope.Validate(); err != nil {
		return op.Error(err, "updating issue report %q", report.ID)
	}

	if err := adoptScope(scope, report); err != nil {
		return op.Error(err, "updating issue report %q", report.ID)
	}

	if err := validReport(report); err != nil {
		return op.Error(err, "updating issue report %q", report.ID)
	}

	count, err := s.q.UpdateReport(ctx, tx, updateReportParams(scope, report))

	return op.Error(
		guardCount(count, err, ErrReportNotFound, "updating the issue report"),
		"updating issue report %q", report.ID)
}

// TransitionReport moves a report from one status to another, through the
// caller's transaction, so the move commits with the entry naming who made it
// and why. See [Store.TransitionReport].
//
// The guard is in the statement, so a caller that lost the race writes nothing
// and learns it. Zero rows means two things — the report moved, or it is not in
// this scope at all — and they are different answers, so the miss is
// disambiguated with a read rather than collapsed. It costs a round trip only on
// the path that already did nothing.
//
// The guard and both reads around it run on tx, so a report filed earlier in the
// same transaction can be moved by this call and the row handed back is the row
// as this transaction has it — which is what a caller recording the outcome
// beside it wants to be describing.
func (s *SQLStore) TransitionReport(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	reportID string,
	from, to Status,
	resolution string,
) (*Report, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(reportIDKey, reportID),
		observability.WithValue(fromStatusKey, from.String()),
		observability.WithValue(statusKey, to.String()),
	)
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "transitioning issue report %q", reportID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "transitioning issue report %q", reportID)
	}

	if err := checkTransition(from, to); err != nil {
		return nil, op.Error(err, "transitioning issue report %q", reportID)
	}

	// The one stamp this store supplies rather than the statement. It is read
	// from the injected clock so a test can put a filing and its resolution a
	// known distance apart; created_at and last_updated_at stay the database's,
	// because they describe the write rather than the report.
	var closedAt *time.Time

	if to.Terminal() {
		closed := s.now()
		closedAt = &closed
	} else {
		// A reopen clears the note with the stamp. A reason that no longer holds
		// is worse than none: it is the answer a support conversation would be
		// conducted from.
		resolution = ""
	}

	affected, err := s.q.TransitionReport(ctx, tx, issuereportsdb.TransitionReportParams{
		Status:        to.String(),
		Resolution:    resolution,
		ClosedAt:      closedAt,
		ID:            reportID,
		Scope:         scope,
		CurrentStatus: from.String(),
	})
	if err != nil {
		return nil, op.Error(err, "transitioning issue report %q", reportID)
	}

	if affected == 0 {
		// The guard matched nothing, which means one of two things: the report
		// moved, or it is not in this scope at all. They are different answers,
		// and only the first is contention — so the read that separates them runs
		// before the miss is reported, and an absent report does not land in the
		// series a dashboard reads as a busy queue. It costs a round trip only on
		// the path that already wrote nothing.
		if _, readErr := s.reportOn(ctx, tx, scope, reportID); readErr != nil {
			return nil, op.Error(readErr, "transitioning issue report %q", reportID)
		}

		return nil, s.guard.Count(ctx, op, affected, nil, reportID,
			"transition", "transitioning the issue report")
	}

	moved, err := s.reportOn(ctx, tx, scope, reportID)
	if err != nil {
		return nil, op.Error(err, "reading back the transitioned issue report")
	}

	return moved, nil
}

// ArchiveReport removes a report from the queue, through the caller's
// transaction, so the removal and whatever the caller records about it commit
// together or not at all. See [Store.ArchiveReport].
//
// Zero rows is ErrReportNotFound rather than a quiet success, and the reading is
// exact: the statement excludes archived rows, so a report that has already been
// archived is not in the queue, which is what this method addresses.
func (s *SQLStore) ArchiveReport(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	reportID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(reportIDKey, reportID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "archiving issue report %q", reportID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "archiving issue report %q", reportID)
	}

	count, err := s.q.ArchiveReport(ctx, tx,
		issuereportsdb.ArchiveReportParams{ID: reportID, Scope: scope})

	return op.Error(
		guardCount(count, err, ErrReportNotFound, "archiving the issue report"),
		"archiving issue report %q", reportID)
}

// DeleteReportsByReporter destroys every report one person filed within the
// scope and reports how many that was.
//
// Zero is not an error. An erasure runs against whatever the subject actually
// left behind, and a person who never filed a report is a person with nothing
// here to erase — reporting that as a failure would fail an erasure that
// succeeded.
func (s *SQLStore) DeleteReportsByReporter(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	reporter string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(reporterKey, reporter),
	)
	defer op.End()

	if tx == nil {
		return 0, op.Error(ErrNilExecutor, "erasing issue reports")
	}

	if err := scope.Validate(); err != nil {
		return 0, op.Error(err, "erasing issue reports")
	}

	if reporter == "" {
		return 0, op.Error(ErrEmptyReporter, "erasing issue reports")
	}

	deleted, err := s.q.DeleteReportsByReporter(ctx, tx,
		issuereportsdb.DeleteReportsByReporterParams{Scope: scope, Reporter: reporter})
	if err != nil {
		return 0, op.Error(err, "erasing issue reports")
	}

	op.Set(countKey, deleted)

	return deleted, nil
}

// checkTransition is the lifecycle, applied before the statement runs.
//
// It is checked here as well as guarded in SQL because the two answer different
// questions. The guard answers "was the row still where you left it", which is a
// race; this answers "is that a move this lifecycle admits", which is a bug. A
// caller that asked to move a resolved report straight to acknowledged would
// otherwise get ErrStatusConflict — a message that says to re-read and try
// again, for a move that will never work.
func checkTransition(from, to Status) error {
	if !from.Valid() {
		return platformerrors.Wrapf(ErrUnknownStatus, "issue report status %q", from)
	}

	if !to.Valid() {
		return platformerrors.Wrapf(ErrUnknownStatus, "issue report status %q", to)
	}

	if !from.CanTransitionTo(to) {
		return platformerrors.Wrapf(ErrInvalidStatusTransition, "%q to %q", from, to)
	}

	return nil
}

// validReport is what the store requires of a row before it writes one.
//
// Three checks, and each refuses a row that would be unreachable rather than
// merely odd: a report filed by nobody is one no reporter's list can find and no
// erasure can reach, one with no kind is one nobody has decided who should look
// at, and one with no details records that somebody was unhappy and nothing
// anyone can act on. The scope is not among them: it is the argument's, checked
// where the write reads it.
func validReport(r *Report) error {
	if r.Reporter == "" {
		return ErrEmptyReporter
	}

	if r.Kind == "" {
		return ErrEmptyKind
	}

	if r.Details == "" {
		return ErrEmptyDetails
	}

	return nil
}
