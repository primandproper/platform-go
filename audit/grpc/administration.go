package grpc

import (
	"context"
	"errors"
	"maps"

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/audit/auditpb"
	"github.com/primandproper/platform-go/v15/callers"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/filtering"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

var _ auditpb.AuditAdministrationServiceServer = (*Server)(nil)

// The resource types an operator's read is filed under: one entry by id, and
// the log as a whole for a page of it.
const (
	auditResourceEntry = "audit_entry"
	auditResourceLog   = "audit_log"
)

// MetadataOperatorOwner is the tenant a ListAnyEntries narrowed its page to,
// on the entry that records the read. It is absent from a read that paged
// every tenant, and present and empty for one narrowed to the global chain —
// which is why it is a key of its own rather than the entry's resource ID,
// where an empty value could not tell those two apart.
const MetadataOperatorOwner = "operator_owner"

// WithOperatorRecorder sets where an operator's read is recorded, and who is
// recorded as making it. Either nil is ignored.
//
// It is what arms AuditAdministrationService at all. A read of another
// tenant's log is exactly the event an audit log exists to show, so a server
// with nowhere to record one answers every call there codes.Unimplemented: the
// methods exist on the wire, and this server does not serve them. The
// principal extractor is here, and nowhere else on this surface, because the
// scope is all AuditService needs to know about a caller and the record is the
// one thing that needs to know who they are.
//
// The record is [audit.OperatorBypassEntry], filed in the connection's scope —
// the operator's own chain — in a transaction of its own and before anything is
// returned. It is the server's own write rather than a caller's, with no
// caller's transaction it could join: the call it records is a read. A record
// that could not be written is the call failing with codes.Internal, rather than
// a read nobody can see.
func WithOperatorRecorder(recorder audit.Recorder, principals callers.PrincipalExtractor) Option {
	return func(s *Server) {
		if recorder != nil && principals != nil {
			s.operatorRecorder, s.principals = recorder, principals
		}
	}
}

// GetAnyEntry reads one entry by id from whichever tenant's chain holds it.
//
// It is audit.Reader.GetAcrossScopes, which AuditService never calls, behind a
// method the deployment's policy grants to its operators — see [Permissions].
// The entry is read before anything is recorded, so an id nobody wrote costs
// the operator a NotFound and the log nothing; an entry that is there is not
// returned until its read has been recorded.
func (s *Server) GetAnyEntry(
	ctx context.Context,
	request *auditpb.GetAnyEntryRequest,
) (*auditpb.GetAnyEntryResponse, error) {
	ctx, req, done, err := s.begin(ctx, auditpb.AuditAdministrationService_GetAnyEntry_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	req.op.Set(entryIDKey, request.GetEntryId())

	principal, err := s.operator(ctx, req)
	if err != nil {
		return nil, err
	}

	entry, err := s.reader.GetAcrossScopes(ctx, s.client.Reader(), request.GetEntryId())
	if err == nil && entry == nil {
		// A reader that answered neither way; see GetEntry.
		err = platformerrors.Wrapf(audit.ErrEntryNotFound, "audit entry %q", request.GetEntryId())
	}

	if err != nil {
		code := codes.Internal
		if errors.Is(err, audit.ErrEntryNotFound) {
			code = codes.NotFound
		}

		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), code, "reading audit entry %q", request.GetEntryId())
	}

	if err = s.recordOperatorRead(ctx, req, principal, auditResourceEntry, request.GetEntryId(), nil); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "recording an operator's read")
	}

	converted, err := ownedEntryToProto(entry)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "converting audit entry %q", request.GetEntryId())
	}

	return &auditpb.GetAnyEntryResponse{Entry: converted}, nil
}

// ListAnyEntries pages every tenant's entries, or one tenant's where the
// request names it, narrowed by the query.
//
// It is recorded before anything is read: a page is a disclosure whatever it
// turns out to hold, including nothing.
func (s *Server) ListAnyEntries(
	ctx context.Context,
	request *auditpb.ListAnyEntriesRequest,
) (*auditpb.ListAnyEntriesResponse, error) {
	ctx, req, done, err := s.begin(ctx, auditpb.AuditAdministrationService_ListAnyEntries_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	principal, err := s.operator(ctx, req)
	if err != nil {
		return nil, err
	}

	filter, err := s.filterFromProto(ctx, req.op, request.GetFilter())
	if err != nil {
		return nil, err
	}

	query := queryFromProto(request.GetQuery())

	var metadata map[string]string
	if request.OwnerId != nil {
		metadata = map[string]string{MetadataOperatorOwner: request.GetOwnerId()}
	}

	if err = s.recordOperatorRead(ctx, req, principal, auditResourceLog, "", metadata); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "recording an operator's read")
	}

	reader := s.client.Reader()

	var page *filtering.QueryFilteredResult[audit.Entry]
	if request.OwnerId != nil {
		// FromOwner rather than Of: an empty owner here is the global chain,
		// named on purpose, and not a lookup that came back empty.
		page, err = s.reader.List(ctx, reader, tenancy.FromOwner(request.GetOwnerId()), query, filter)
	} else {
		page, err = s.reader.ListAcrossScopes(ctx, reader, query, filter)
	}

	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "listing audit entries")
	}

	results := make([]*auditpb.OwnedEntry, 0, len(page.Data))
	for _, entry := range page.Data {
		converted, convErr := ownedEntryToProto(entry)
		if convErr != nil {
			err = convErr

			return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "converting audit entries")
		}

		results = append(results, converted)
	}

	return &auditpb.ListAnyEntriesResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    results,
	}, nil
}

// operator answers who is making an administration call, refusing it where
// this server has nowhere to record one.
//
// Whether they may make it at all is not asked here. The method is the
// capability, and the deployment's authorization interceptor has already
// decided it against [Permissions] before the handler runs.
func (s *Server) operator(ctx context.Context, req *request) (callers.Principal, error) {
	if s.operatorRecorder == nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(ErrOperatorReadsUnrecorded, req.op.Logger(), req.op.Span(),
			codes.Unimplemented, "serving an operator's read")
	}

	principal, ok := s.principals(ctx)
	if !ok || principal == nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(callers.ErrNoPrincipal, req.op.Logger(), req.op.Span(),
			codes.Unauthenticated, "identifying the operator")
	}

	return principal, nil
}

// recordOperatorRead files an operator's read in the connection's chain.
func (s *Server) recordOperatorRead(
	ctx context.Context,
	req *request,
	principal callers.Principal,
	resourceType, resourceID string,
	metadata map[string]string,
) error {
	method, _ := grpc.Method(ctx)
	entry := audit.OperatorBypassEntry(principal, PermissionReadAnyEntries, method, resourceType, resourceID)

	maps.Copy(entry.Metadata, metadata)

	if err := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.operatorRecorder.Record(ctx, tx, req.scope, entry)
	}); err != nil {
		return platformerrors.Wrapf(err, "recording an operator's read of %s %q", resourceType, resourceID)
	}

	return nil
}

// ownedEntryToProto renders an entry with the tenant whose chain holds it.
func ownedEntryToProto(entry *audit.Entry) (*auditpb.OwnedEntry, error) {
	converted, err := EntryToProto(entry)
	if err != nil {
		return nil, err
	}

	return &auditpb.OwnedEntry{Entry: converted, OwnerId: entry.Scope.Owner()}, nil
}
