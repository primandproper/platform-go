package issuereports

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The sentinels this package returns. They live together because a caller
// deciding what to do next is choosing between them, and a set spread across the
// files that happen to return each one cannot be read as the set it is.
var (
	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil issue reports database client")

	// ErrNilReport indicates a nil *Report where one was required.
	ErrNilReport = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil issue report")

	// ErrNilExecutor indicates a nil executor. Every method here runs on one the
	// caller supplies — a database.Tx for a write, an executor for a read — so
	// there is no method that can fall back to a connection of the store's own.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil issue reports query executor")

	// ErrScopeMismatch indicates a write whose Report.Scope names a different
	// tenant than the scope the call named.
	//
	// The argument is what the statement binds, so the two disagreeing is a
	// caller holding one tenant's report and writing it into another — either a
	// stale value or a mix-up, and neither is a thing to guess at. An unset
	// field adopts the argument; a set one that disagrees is refused rather than
	// corrected, which is the same reading this package already takes of a
	// status a create arrives with.
	ErrScopeMismatch = platformerrors.New("issue report names a different scope than the write")

	// ErrReportNotFound indicates a report that does not exist in the scope that
	// asked. One belonging to another scope reads as absent — which is what it
	// is from here, and is the answer that does not turn a read into an oracle
	// for what other tenants have been told.
	ErrReportNotFound = platformerrors.New("issue report not found")

	// ErrStatusConflict indicates a transition whose guard matched no row
	// because the report had already moved: two triagers resolving the same
	// report, or a reopen racing a resolve.
	//
	// It is distinct from ErrReportNotFound because the two ask different things
	// of the caller. A report that is not there is nothing to retry; a report
	// that moved is one whose current status the caller should re-read and
	// decide about again.
	ErrStatusConflict = platformerrors.New("issue report is no longer in the expected status")

	// ErrInvalidStatusTransition indicates a move the lifecycle does not admit —
	// including creating a report in any status but open, which is a transition
	// spelled as a create.
	ErrInvalidStatusTransition = platformerrors.New("invalid issue report status transition")

	// ErrUnknownStatus indicates a status this package does not serve. It is
	// refused rather than stored, because a report in a status no queue lists is
	// a report nobody will ever see again.
	ErrUnknownStatus = platformerrors.New("unknown issue report status")

	// ErrEmptyReporter indicates a report filed by nobody.
	//
	// It is refused rather than stored, because the empty reporter is not a
	// wildcard and is not "anonymous": a report filed under it is one no
	// reporter's list can find and one no subject access request can collect or
	// erase.
	ErrEmptyReporter = platformerrors.New("empty issue report reporter")

	// ErrEmptyKind indicates a report filed under no category. A kind is what a
	// triage queue groups and routes by, so a report without one is one nobody
	// has decided who should look at.
	ErrEmptyKind = platformerrors.New("empty issue report kind")

	// ErrEmptyDetails indicates a report with nothing in it. The details are the
	// report; a row without them records that somebody was unhappy and nothing
	// anyone can act on.
	ErrEmptyDetails = platformerrors.New("empty issue report details")
)
