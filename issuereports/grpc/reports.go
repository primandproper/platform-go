package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/issuereports"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/primandproper/primitives-go/database"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"

	"google.golang.org/grpc/codes"
)

// The five RPCs that name one report: the filing, the read, the revision, the
// move and the archive.
//
// Each write is one call inside one transaction, because issuereports' writes
// take a database.Tx and an RPC handler is the caller with nothing of its own to
// join — which is the case that store's documentation describes when it says a
// consumer with nothing to commit alongside opens one with Client.WithTransaction
// and passes the Tx it is handed.
//
// None of them switches on a sentinel: the error goes through
// grpcerrors.PrepareAndLogGRPCStatus with codes.Internal as the *default*, and
// the encoding interceptor re-runs the registered mappers over the preserved
// chain, so issuereports.GRPCMapper wins over the guess made here. The two
// places a code is passed as an answer rather than as a default are the request
// that named no input, which nothing maps, and a refusal from the authorizer.

// CreateReport files a report in the caller's name.
//
// The reporter comes off the principal and the request cannot carry one:
// IssueReportCreationInput reserves the field, so a report filed in somebody
// else's words is not something a client can ask for. The status is not read
// off the request either — a report is born open, and the store refuses a value
// that says otherwise, because a report that started resolved is one nobody
// resolved.
func (s *Server) CreateReport(
	ctx context.Context,
	request *issuereportspb.CreateReportRequest,
) (*issuereportspb.CreateReportResponse, error) {
	ctx, req, done, err := s.caller(ctx, issuereportspb.IssueReportsService_CreateReport_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	report := reportFromCreationInput(request.GetInput(), req.principal.UserID())
	if report == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilReportInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "filing an issue report")

		return nil, err
	}

	req.op.Set(reporterKey, report.Reporter).
		Set(subjectTypeKey, report.SubjectType).
		Set(subjectIDKey, report.SubjectID)

	// The store answers with the row it wrote — the identifier it minted, the
	// status a report is born in, the creation time the database assigned — so
	// the response is what was stored rather than the request that asked for it,
	// and this handler needs no read-back of its own.
	var stored *issuereports.Report

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		created, createErr := s.store.CreateReport(ctx, tx, req.scope, report)
		if createErr != nil {
			return createErr
		}

		stored = created

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "filing an issue report")

		return nil, err
	}

	req.op.Set(reportIDKey, stored.ID)

	return &issuereportspb.CreateReportResponse{Result: ReportToProto(stored)}, nil
}

// GetReport reads one of the tenant's live reports.
//
// A report in another tenant's scope reads as one that does not exist, which is
// what it is from here — the scope is bound into the statement rather than
// checked in front of it. A report in this tenant that the caller may not read
// reads the same way, and that is [Server.authorizeReadReport]'s doing: the read
// has already happened by the time the question is asked, so a refusal that said
// so would tell a caller walking report ids which of them are real.
func (s *Server) GetReport(
	ctx context.Context,
	request *issuereportspb.GetReportRequest,
) (*issuereportspb.GetReportResponse, error) {
	ctx, req, done, err := s.caller(ctx, issuereportspb.IssueReportsService_GetReport_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetReportId()
	req.op.Set(reportIDKey, id)

	report, err := s.store.GetReport(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading issue report %q", id)

		return nil, err
	}

	if err = s.authorizeReadReport(ctx, req, report,
		"authorizing the caller against issue report %q", id); err != nil {
		return nil, err
	}

	return &issuereportspb.GetReportResponse{Result: ReportToProto(report)}, nil
}

// UpdateReport revises what a report says: the kind, the details, and what it is
// about.
//
// It does not move the status, and it cannot: the lifecycle has one door and it
// is [Server.TransitionReport], which names the status it believed the row was
// in. IssueReportUpdateInput reserves the status and the resolution for that
// reason, and reserves the reporter because a revision does not change whose
// words these were.
//
// Two calls inside one transaction, and each earns its round trip. The read is
// what supplies the reporter the store validates against — the request has none
// and must not — and it is what makes an absent report an absence before
// anything is written. The revision is the write, and it answers with the stored
// row, because last_updated_at is the database's and a response assembled from
// the request would say the row was last touched at the epoch. Both run on the
// transaction, so the row that comes back is the one the write just left.
func (s *Server) UpdateReport(
	ctx context.Context,
	request *issuereportspb.UpdateReportRequest,
) (*issuereportspb.UpdateReportResponse, error) {
	ctx, req, done, err := s.caller(ctx, issuereportspb.IssueReportsService_UpdateReport_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetReportId()
	req.op.Set(reportIDKey, id)

	if request.GetInput() == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilReportInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "revising issue report %q", id)

		return nil, err
	}

	var revised *issuereports.Report

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		report, readErr := s.store.GetReport(ctx, tx, req.scope, id)
		if readErr != nil {
			return readErr
		}

		applyUpdateInput(report, request.GetInput())

		stored, updateErr := s.store.UpdateReport(ctx, tx, req.scope, report)
		if updateErr != nil {
			return updateErr
		}

		revised = stored

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "revising issue report %q", id)

		return nil, err
	}

	return &issuereportspb.UpdateReportResponse{Result: ReportToProto(revised)}, nil
}

// TransitionReport moves a report from the status the caller believed it held to
// the one it should hold now, and hands back the row as stored.
//
// Both statuses travel on the wire because the guard is the point: the statement
// requires the row to still hold the first, so two triagers resolving the same
// report means one of them is refused rather than both being told they won and
// the second note overwriting the first. The refusal is
// issuereports.ErrStatusConflict, which reaches a client as codes.Aborted and
// means re-read and decide again — distinct from an absence, which is nothing to
// retry.
//
// A move the lifecycle does not admit is refused before anything is written, and
// a status this queue does not have is refused rather than matched against no
// row: STATUS_UNSPECIFIED converts to the empty status, which is
// issuereports.ErrUnknownStatus.
func (s *Server) TransitionReport(
	ctx context.Context,
	request *issuereportspb.TransitionReportRequest,
) (*issuereportspb.TransitionReportResponse, error) {
	ctx, req, done, err := s.caller(ctx, issuereportspb.IssueReportsService_TransitionReport_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	var (
		id   = request.GetReportId()
		from = StatusFromProto(request.GetExpectedStatus())
		to   = StatusFromProto(request.GetTargetStatus())
	)

	req.op.Set(reportIDKey, id).
		Set(fromStatusKey, from.String()).
		Set(statusKey, to.String())

	var moved *issuereports.Report

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		report, transitionErr := s.store.TransitionReport(ctx, tx, req.scope, id, from, to, request.GetResolution())
		if transitionErr != nil {
			return transitionErr
		}

		moved = report

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal,
			"moving issue report %q from %q to %q", id, from, to)

		return nil, err
	}

	return &issuereportspb.TransitionReportResponse{Result: ReportToProto(moved)}, nil
}

// ArchiveReport removes a report from the queue, leaving the row for whoever
// asks later what was reported.
//
// It is not closing. Closing is a status a triager moves to and it stays in the
// queue; this hides the row, which is what a test submission or a duplicate
// somebody filed twice by refreshing wants. A report already archived is an
// absence, which is the store's own reading: an archived report is not in the
// queue and this method addresses the queue.
func (s *Server) ArchiveReport(
	ctx context.Context,
	request *issuereportspb.ArchiveReportRequest,
) (*issuereportspb.ArchiveReportResponse, error) {
	ctx, req, done, err := s.caller(ctx, issuereportspb.IssueReportsService_ArchiveReport_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetReportId()
	req.op.Set(reportIDKey, id)

	// The store answers with the row it hid, and nothing here carries it:
	// ArchiveReportResponse has no field for a report, because an archive on the
	// wire says only that the report left the queue. The row is for a consumer
	// writing an entry beside the write, which this handler is not — its
	// transaction holds this one call and nothing to describe it to.
	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		_, archiveErr := s.store.ArchiveReport(ctx, tx, req.scope, id)

		return archiveErr
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving issue report %q", id)

		return nil, err
	}

	return &issuereportspb.ArchiveReportResponse{}, nil
}
