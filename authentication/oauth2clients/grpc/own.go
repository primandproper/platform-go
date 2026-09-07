package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	grpcerrors "github.com/primandproper/platform-go/v14/errors/grpc"
	filteringgrpc "github.com/primandproper/platform-go/v14/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The self-service half of the surface: five RPCs over the registrations the
// caller owns, none of them behind a permission.
//
// Owning the row is the authorization. Both the registry and the owner come off
// the principal, so there is nothing in any of these requests that could name
// somebody else's registration — which is why they are in [SelfServiceMethods]
// and not in [Permissions], and why they are separate methods rather than a
// field on the administered ones. See the .proto for the long form.
//
// The three that name a row read it first and compare its owner. That check is
// here rather than in the store because it is a transport decision — the store
// is told which owner to page by, and these methods are what decides that the
// owner is the caller.

// CreateOwnOAuth2Client mints a registration the caller owns.
func (s *Server) CreateOwnOAuth2Client(
	ctx context.Context,
	request *oauth2clientspb.CreateOwnOAuth2ClientRequest,
) (*oauth2clientspb.CreateOwnOAuth2ClientResponse, error) {
	ctx, req, done, err := s.caller(ctx, oauth2clientspb.OAuth2ClientsService_CreateOwnOAuth2Client_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	input := creationInputFromProto(request.GetInput())
	if input == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(oauth2clients.ErrNilInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "creating an oauth2 client")

		return nil, err
	}

	issued, err := s.svc.CreateClient(ctx, req.scope, req.principal.UserID(), input)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "creating an oauth2 client")

		return nil, err
	}

	return &oauth2clientspb.CreateOwnOAuth2ClientResponse{Issued: IssuedClientToProto(issued)}, nil
}

// GetOwnOAuth2Client reads one registration the caller owns.
func (s *Server) GetOwnOAuth2Client(
	ctx context.Context,
	request *oauth2clientspb.GetOwnOAuth2ClientRequest,
) (*oauth2clientspb.GetOwnOAuth2ClientResponse, error) {
	ctx, req, done, err := s.caller(ctx, oauth2clientspb.OAuth2ClientsService_GetOwnOAuth2Client_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	client, err := s.own(ctx, req, request.GetOauth2ClientId())
	if err != nil {
		return nil, err
	}

	return &oauth2clientspb.GetOwnOAuth2ClientResponse{Result: ClientToProto(client)}, nil
}

// ListOwnOAuth2Clients pages the registrations the caller owns.
func (s *Server) ListOwnOAuth2Clients(
	ctx context.Context,
	request *oauth2clientspb.ListOwnOAuth2ClientsRequest,
) (*oauth2clientspb.ListOwnOAuth2ClientsResponse, error) {
	ctx, req, done, err := s.caller(ctx, oauth2clientspb.OAuth2ClientsService_ListOwnOAuth2Clients_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of an oauth2 client page")

		return nil, err
	}

	page, err := s.store.ListClientsForOwner(ctx, s.client.Reader(), req.scope, req.principal.UserID(), filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing a user's oauth2 clients")

		return nil, err
	}

	return &oauth2clientspb.ListOwnOAuth2ClientsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ClientsToProto(page.Data),
	}, nil
}

// UpdateOwnOAuth2Client revises one registration the caller owns.
func (s *Server) UpdateOwnOAuth2Client(
	ctx context.Context,
	request *oauth2clientspb.UpdateOwnOAuth2ClientRequest,
) (*oauth2clientspb.UpdateOwnOAuth2ClientResponse, error) {
	ctx, req, done, err := s.caller(ctx, oauth2clientspb.OAuth2ClientsService_UpdateOwnOAuth2Client_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetOauth2ClientId()

	input := updateInputFromProto(request.GetInput())
	if input == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(oauth2clients.ErrNilInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "updating oauth2 client %q", id)

		return nil, err
	}

	if _, err = s.own(ctx, req, id); err != nil {
		return nil, err
	}

	client, err := s.svc.UpdateClient(ctx, req.scope, id, input)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "updating oauth2 client %q", id)

		return nil, err
	}

	return &oauth2clientspb.UpdateOwnOAuth2ClientResponse{Result: ClientToProto(client)}, nil
}

// ArchiveOwnOAuth2Client withdraws one registration the caller owns.
func (s *Server) ArchiveOwnOAuth2Client(
	ctx context.Context,
	request *oauth2clientspb.ArchiveOwnOAuth2ClientRequest,
) (*oauth2clientspb.ArchiveOwnOAuth2ClientResponse, error) {
	ctx, req, done, err := s.caller(ctx, oauth2clientspb.OAuth2ClientsService_ArchiveOwnOAuth2Client_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetOauth2ClientId()

	if _, err = s.own(ctx, req, id); err != nil {
		return nil, err
	}

	if err = s.svc.ArchiveClient(ctx, req.scope, id); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving oauth2 client %q", id)

		return nil, err
	}

	return &oauth2clientspb.ArchiveOwnOAuth2ClientResponse{}, nil
}

// own reads a registration and reports it only if the caller owns it.
//
// It is the whole of the self-service half's authorization, in one place so the
// three methods that name a row cannot implement it three ways.
//
// A registration the caller does not own answers oauth2clients.ErrOwnerMismatch,
// which the package's mapper turns into PermissionDenied with the message "no
// such oauth2 client". That the message matches the not-found one is deliberate:
// on this half, "somebody else's" and "does not exist" are the same answer,
// because telling them apart would let anybody enumerate the registry's rows one
// identifier at a time.
//
// An administered registration — one nobody owns — is not the caller's either.
// Withdrawing the credential an operator minted for the whole deployment is not
// something the self-service door does, and the administered methods are where
// somebody with the grant does it.
func (s *Server) own(ctx context.Context, req *request, id string) (*oauth2clients.Client, error) {
	req.op.Set(clientKey, id)

	client, err := s.store.GetClient(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading oauth2 client %q", id)
	}

	if client.BelongsToUser != req.principal.UserID() {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(
			platformerrors.Wrapf(oauth2clients.ErrOwnerMismatch, "oauth2 client %q", id),
			req.op.Logger(), req.op.Span(), codes.PermissionDenied, "reading oauth2 client %q", id)
	}

	return client, nil
}
