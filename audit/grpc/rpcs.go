package grpc

import (
	"context"
	"errors"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"
	"github.com/primandproper/primitives-go/v2/observability"

	"google.golang.org/grpc/codes"
)

// GetEntry reads one entry by id, out of the log the connection is against.
//
// The scope goes into the read rather than into a comparison after it. The
// reader's Get takes a *tenancy.Scope in which nil is the operator's read
// across every tenant; this surface has no operator and never passes nil, so an
// entry belonging to somebody else is not read at all and is answered exactly
// as an id that does not exist is — audit.ErrEntryNotFound, mapped to
// codes.NotFound. Telling the two apart would make this an oracle for which
// entry ids exist in another tenant's log, which is the one thing an audit log
// must not become, and answering it inside the read is what makes that true of
// every caller of Get rather than of this method.
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

	scopes, err := s.chainsFor(ctx, req.scope)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(),
			codes.Internal, "resolving the chains to read")
	}

	// Across the same chains ListEntries spans, so a caller who finds one of
	// their own entries on a page can ask for it by id. See chains.go.
	entry, err := s.getAcrossChains(ctx, s.client.Reader(), scopes, request.GetEntryId())
	if err != nil {
		// Not found is the caller's answer rather than a failure, and it is the
		// one this can raise itself: every other error is the reader's.
		code := codes.Internal
		if errors.Is(err, audit.ErrEntryNotFound) {
			code = codes.NotFound
		}

		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), code, "reading audit entry %q", request.GetEntryId())
	}

	// A nil entry with a nil error is a reader that answered neither way. The
	// SQL one cannot, but audit.Reader is a seam a consumer may implement, and
	// a nil dereference in the converter below is a worse account of that than
	// the answer this surface gives for an entry it will not describe.
	if entry == nil {
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

	// Which chains this read spans. One is the ordinary answer and the only one
	// a deployment with a chain per tenant ever gets; a deployment that files
	// some entries per actor has more, and chains.go says why and how the pages
	// are merged.
	scopes, err := s.chainsFor(ctx, req.scope)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(),
			codes.Internal, "resolving the chains to read")
	}

	page, err := s.listAcrossChains(ctx, s.client.Reader(), scopes, query, filter)
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
//
// One call walks as much of the chain as the reader's verification ceiling
// allows and says where it stopped, which is what makes the scheduled use safe
// to expose: both ends of the window are optional here, so a request naming
// neither asks for a scope's whole history. A client that wants all of it sends
// the same window again with after_seq set to the previous response's last_seq,
// and the server checks the link across that seam like any other.
func (s *Server) VerifyChain(
	ctx context.Context,
	request *auditpb.VerifyChainRequest,
) (*auditpb.VerifyChainResponse, error) {
	ctx, req, done, err := s.begin(ctx, auditpb.AuditService_VerifyChain_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	// An absent after_seq is audit.ChainStart rather than the zero GetAfterSeq
	// would hand back, because 0 is a position a chain actually holds — its
	// first — and reading an unset field as it would skip the genesis entry of
	// every chain this surface ever verified.
	afterSeq := audit.ChainStart
	if request.AfterSeq != nil {
		afterSeq = request.GetAfterSeq()
	}

	result, err := s.reader.Verify(ctx, s.client.Reader(), req.scope,
		timeFromProto(request.GetFrom()), timeFromProto(request.GetTo()), afterSeq)
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
