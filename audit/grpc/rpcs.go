package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"

	platformerrors "github.com/primandproper/primitives-go/errors"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/filtering/filteringpb"
	filteringgrpc "github.com/primandproper/primitives-go/filtering/grpc"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/pointer"

	"google.golang.org/grpc/codes"
)

// GetEntry reads one entry by id.
//
// The reader's Get takes an id and no scope, because in a process it is an
// operator's read against a reader they built. Here the entry's scope is
// compared against the connection's afterwards, and one belonging to somebody
// else is answered exactly as an id that does not exist is: audit.ErrEntryNotFound,
// mapped to codes.NotFound. Telling the two apart would make this an oracle for
// which entry ids exist in another tenant's log, which is the one thing an audit
// log must not become.
func (s *Server) GetEntry(
	ctx context.Context,
	request *auditpb.GetEntryRequest,
) (*auditpb.GetEntryResponse, error) {
	ctx, req, done, err := s.begin(ctx, auditpb.AuditService_GetEntry_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	req.op.Set(entryIDKey, request.GetEntryId())

	entry, err := s.reader.Get(ctx, request.GetEntryId())
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "reading audit entry %q", request.GetEntryId())
	}

	if entry == nil || entry.Scope != req.scope.Owner() {
		err = grpcerrors.PrepareAndLogGRPCStatus(
			platformerrors.Wrapf(audit.ErrEntryNotFound, "audit entry %q", request.GetEntryId()),
			req.op.Logger(), req.op.Span(), codes.NotFound, "reading audit entry %q", request.GetEntryId())

		return nil, err
	}

	converted, err := EntryToProto(entry)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "converting audit entry %q", request.GetEntryId())
	}

	return &auditpb.GetEntryResponse{Entry: converted}, nil
}

// ListEntries pages the caller's log, narrowed by whatever the query asked for.
//
// The scope is written onto the query here, after the conversion and over
// nothing, because the message it was converted from has no scope field to
// carry one. That assignment is the whole of this service's tenancy: an
// audit.Query with a nil Scope reads every tenant's events, and it is nil for
// exactly as long as it takes this line to run.
func (s *Server) ListEntries(
	ctx context.Context,
	request *auditpb.ListEntriesRequest,
) (*auditpb.ListEntriesResponse, error) {
	ctx, req, done, err := s.begin(ctx, auditpb.AuditService_ListEntries_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := s.filterFromProto(req.op, request.GetFilter())
	if err != nil {
		return nil, err
	}

	query := queryFromProto(request.GetQuery())
	query.Scope = pointer.To(req.scope.Owner())

	page, err := s.reader.List(ctx, query, filter)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "listing audit entries")
	}

	results, err := EntriesToProto(page.Data)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "converting audit entries")
	}

	return &auditpb.ListEntriesResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    results,
	}, nil
}

// VerifyChain walks the caller's chain over a time range and reports the first
// break, or that there was none.
//
// It is the reason this surface exists. Reading entries is something a consumer
// can approximate over their own log; establishing that nobody edited, removed
// or reordered one is not, and it is the call most worth making remotely and on
// a schedule.
//
// A break is an ordinary response rather than an error status. It is a finding
// about the data and not a failure to answer the question — audit.ErrChainBroken
// exists for a caller escalating one, and is deliberately not what this returns.
func (s *Server) VerifyChain(
	ctx context.Context,
	request *auditpb.VerifyChainRequest,
) (*auditpb.VerifyChainResponse, error) {
	ctx, req, done, err := s.begin(ctx, auditpb.AuditService_VerifyChain_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	result, err := s.reader.Verify(ctx, req.scope, timeFromProto(request.GetFrom()), timeFromProto(request.GetTo()))
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "verifying an audit chain")
	}

	return &auditpb.VerifyChainResponse{Result: VerificationResultToProto(result)}, nil
}

// filterFromProto reads a query filter, in the one place the paged read does.
//
// A malformed filter is codes.InvalidArgument rather than the Internal every
// other failure here defaults to: it is the one thing on these requests a
// client can get wrong on its own, and the filtering converters already
// distinguish it. An absent filter is the default page rather than an error.
func (s *Server) filterFromProto(
	op observability.Operation,
	in *filteringpb.QueryFilter,
) (*filtering.QueryFilter, error) {
	filter, err := filteringgrpc.FromProto(in)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.InvalidArgument, "reading the query filter")
	}

	return filter, nil
}
