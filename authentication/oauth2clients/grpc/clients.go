package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	grpcerrors "github.com/primandproper/platform-go/v14/errors/grpc"
	filteringgrpc "github.com/primandproper/platform-go/v14/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The whole of the surface: four RPCs over any registration in the caller's
// registry, each behind a permission.
//
// Every one of them takes the registry off the caller's principal, and none of
// them takes an owner at all — an administered registration belongs to nobody,
// which is what [oauth2clients.Client.Admits] reads as "any subject in this
// registry may authorize through it". Minting one is the sharpest thing this
// service does, and PermissionCreateClients is the grant that says so.
//
// Each method is one call plus its conversion. The error goes through
// grpcerrors.PrepareAndLogGRPCStatus with codes.Internal as the *default*: the
// encoding interceptor re-runs the registered mappers over the preserved chain,
// so oauth2clients.GRPCMapper wins over the guess made here and no handler on
// this surface switches on a sentinel.

// CreateOAuth2Client mints a registration that belongs to no person.
func (s *Server) CreateOAuth2Client(
	ctx context.Context,
	request *oauth2clientspb.CreateOAuth2ClientRequest,
) (*oauth2clientspb.CreateOAuth2ClientResponse, error) {
	ctx, req, done, err := s.caller(ctx, oauth2clientspb.OAuth2ClientsService_CreateOAuth2Client_FullMethodName)
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

	// The empty owner is the administered arrangement, spelled here rather than
	// read off the request: an owner a client could name is a credential minted
	// in somebody else's name.
	issued, err := s.svc.CreateClient(ctx, req.scope, "", input)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "creating an oauth2 client")

		return nil, err
	}

	return &oauth2clientspb.CreateOAuth2ClientResponse{Issued: IssuedClientToProto(issued)}, nil
}

// GetOAuth2Client reads any live registration in the caller's registry.
func (s *Server) GetOAuth2Client(
	ctx context.Context,
	request *oauth2clientspb.GetOAuth2ClientRequest,
) (*oauth2clientspb.GetOAuth2ClientResponse, error) {
	ctx, req, done, err := s.caller(ctx, oauth2clientspb.OAuth2ClientsService_GetOAuth2Client_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetOauth2ClientId()
	req.op.Set(clientKey, id)

	client, err := s.store.GetClient(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading oauth2 client %q", id)

		return nil, err
	}

	return &oauth2clientspb.GetOAuth2ClientResponse{Result: ClientToProto(client)}, nil
}

// ListOAuth2Clients pages every registration in the caller's registry.
func (s *Server) ListOAuth2Clients(
	ctx context.Context,
	request *oauth2clientspb.ListOAuth2ClientsRequest,
) (*oauth2clientspb.ListOAuth2ClientsResponse, error) {
	ctx, req, done, err := s.caller(ctx, oauth2clientspb.OAuth2ClientsService_ListOAuth2Clients_FullMethodName)
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

	page, err := s.store.ListClients(ctx, s.client.Reader(), req.scope, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing oauth2 clients")

		return nil, err
	}

	return &oauth2clientspb.ListOAuth2ClientsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ClientsToProto(page.Data),
	}, nil
}

// ArchiveOAuth2Client withdraws any live registration in the caller's registry.
func (s *Server) ArchiveOAuth2Client(
	ctx context.Context,
	request *oauth2clientspb.ArchiveOAuth2ClientRequest,
) (*oauth2clientspb.ArchiveOAuth2ClientResponse, error) {
	ctx, req, done, err := s.caller(ctx, oauth2clientspb.OAuth2ClientsService_ArchiveOAuth2Client_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetOauth2ClientId()
	req.op.Set(clientKey, id)

	if err = s.svc.ArchiveClient(ctx, req.scope, id); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving oauth2 client %q", id)

		return nil, err
	}

	return &oauth2clientspb.ArchiveOAuth2ClientResponse{}, nil
}
