package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The five RPCs that page reports: the queue whole, the queue by status, one
// person's list, everything about a kind of thing, and everything about one
// particular thing.
//
// Four of the five are the triager's, and their target is the queue rather than
// a person: the grant on the method is the whole of the answer to "whose", which
// is what PermissionTriageReports means. The fifth names a person and is
// therefore the one that asks [ReportAuthorizer], before it reads anything —
// there is nothing to read yet, and the name is the thing being gated.
//
// Every one of them pages within the caller's tenant and none of them can be
// asked to cross one. There is no cross-scope listing in issuereports.Store at
// all, and its documentation is clear about what that costs an operator: they
// list the scopes they administer and page each. A read that omitted the scope
// is the one read that cannot tell an operator's caller from a tenant's.
//
// A filter no converter can read is answered as malformed, before anything is
// gated or read: saying so discloses nothing about any row.

// ListReports pages every report in the caller's tenant.
func (s *Server) ListReports(
	ctx context.Context,
	request *issuereportspb.ListReportsRequest,
) (*issuereportspb.ListReportsResponse, error) {
	ctx, req, done, err := s.caller(ctx, issuereportspb.IssueReportsService_ListReports_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of an issue report page")

		return nil, err
	}

	page, err := s.store.ListReports(ctx, s.client.Reader(), req.scope, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing issue reports")

		return nil, err
	}

	return &issuereportspb.ListReportsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ReportsToProto(page.Data),
	}, nil
}

// ListReportsByStatus pages one queue: everything open, everything acknowledged,
// everything resolved, everything declined.
//
// A status this queue does not have is refused rather than answered with an
// empty page, because an empty page is what a queue that has been quietly
// misspelled looks like — and STATUS_UNSPECIFIED is one of those, not a request
// for all four.
//
// The count a console wants beside the queue is on the response's pagination:
// the filtered count is of everything in that status, not of the page.
func (s *Server) ListReportsByStatus(
	ctx context.Context,
	request *issuereportspb.ListReportsByStatusRequest,
) (*issuereportspb.ListReportsByStatusResponse, error) {
	ctx, req, done, err := s.caller(ctx, issuereportspb.IssueReportsService_ListReportsByStatus_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	status := StatusFromProto(request.GetStatus())
	req.op.Set(statusKey, status.String())

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of an issue report queue")

		return nil, err
	}

	page, err := s.store.ListReportsByStatus(ctx, s.client.Reader(), req.scope, status, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing %q issue reports", status)

		return nil, err
	}

	return &issuereportspb.ListReportsByStatusResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ReportsToProto(page.Data),
	}, nil
}

// ListReportsByReporter pages the reports one person filed.
//
// It is the one read on this surface whose request names a person, so it is the
// one that asks the consumer's rule whether this caller may name them —
// somebody's own list and a triager's read of somebody's list are the same call
// with different standing behind it. The refusal is codes.PermissionDenied and
// tells the caller nothing about whether that person has ever filed anything:
// the question is asked before the read.
func (s *Server) ListReportsByReporter(
	ctx context.Context,
	request *issuereportspb.ListReportsByReporterRequest,
) (*issuereportspb.ListReportsByReporterResponse, error) {
	ctx, req, done, err := s.caller(ctx, issuereportspb.IssueReportsService_ListReportsByReporter_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	reporter := request.GetReporter()
	req.op.Set(reporterKey, reporter)

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a reporter's issue reports")

		return nil, err
	}

	if err = s.authorizeNamedReporter(ctx, req, reporter,
		"authorizing the caller against the issue reports of %q", reporter); err != nil {
		return nil, err
	}

	page, err := s.store.ListReportsByReporter(ctx, s.client.Reader(), req.scope, reporter, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing the issue reports of %q", reporter)

		return nil, err
	}

	return &issuereportspb.ListReportsByReporterResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ReportsToProto(page.Data),
	}, nil
}

// ListReportsBySubjectType pages every report about one kind of thing —
// everything anybody has said about recipes.
//
// The subject type is the application's own word and nothing here validates it:
// one this deployment does not use is an empty page rather than a refusal, which
// is the honest answer for a vocabulary this package does not own.
func (s *Server) ListReportsBySubjectType(
	ctx context.Context,
	request *issuereportspb.ListReportsBySubjectTypeRequest,
) (*issuereportspb.ListReportsBySubjectTypeResponse, error) {
	ctx, req, done, err := s.caller(ctx, issuereportspb.IssueReportsService_ListReportsBySubjectType_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	subjectType := request.GetSubjectType()
	req.op.Set(subjectTypeKey, subjectType)

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a subject type's issue reports")

		return nil, err
	}

	page, err := s.store.ListReportsBySubjectType(ctx, s.client.Reader(), req.scope, subjectType, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing issue reports about %q", subjectType)

		return nil, err
	}

	return &issuereportspb.ListReportsBySubjectTypeResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ReportsToProto(page.Data),
	}, nil
}

// ListReportsForSubject pages every report about one particular thing, which is
// what a moderation view of that thing renders.
func (s *Server) ListReportsForSubject(
	ctx context.Context,
	request *issuereportspb.ListReportsForSubjectRequest,
) (*issuereportspb.ListReportsForSubjectResponse, error) {
	ctx, req, done, err := s.caller(ctx, issuereportspb.IssueReportsService_ListReportsForSubject_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	var (
		subjectType = request.GetSubjectType()
		subjectID   = request.GetSubjectId()
	)

	req.op.Set(subjectTypeKey, subjectType).Set(subjectIDKey, subjectID)

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a subject's issue reports")

		return nil, err
	}

	page, err := s.store.ListReportsForSubject(ctx, s.client.Reader(), req.scope, subjectType, subjectID, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal,
			"listing issue reports about %q %q", subjectType, subjectID)

		return nil, err
	}

	return &issuereportspb.ListReportsForSubjectResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ReportsToProto(page.Data),
	}, nil
}
