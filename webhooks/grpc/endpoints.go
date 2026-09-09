package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/webhooks"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/primandproper/primitives-go/database"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The endpoint half of the surface: four RPCs over the endpoints registered in
// the caller's tenant, each behind a permission.
//
// Every one of them takes the tenant off the caller's principal, and none of
// them takes a scope at all. The three reads and the archive are one call each;
// the save is one call inside one transaction, because webhooks.Dispatcher's
// writes take a database.Tx and an RPC handler is the caller with nothing of its
// own to join.
//
// None of them switches on a sentinel: the error goes through
// grpcerrors.PrepareAndLogGRPCStatus with codes.Internal as the *default*, and
// the encoding interceptor re-runs the registered mappers over the preserved
// chain, so webhooks.GRPCMapper wins over the guess made here. The two places a
// code is passed as an answer rather than a default are the two refusals nothing
// maps: a save that named no endpoint, and one that named no signing keys.

// SaveEndpoint registers an endpoint in the caller's tenant, or re-registers one
// that is already there, reconciling its subscriptions against the event types
// the request names.
//
// It writes through webhooks.Dispatcher.Register rather than through the store,
// because Register is where the URL is checked: an endpoint is a URL a user
// supplied that this deployment will then make authenticated requests to, and
// validation of it "is not optional and not separable."
//
// The signing keys are required and are checked here, before the transaction is
// opened. That check is at this call site rather than in webhooks.GRPCMapper
// because the sentinel it raises is requestsigning's — a keyring with no key is
// a wiring failure everywhere else in the process, and a mapper case would
// answer for all of those too.
func (s *Server) SaveEndpoint(
	ctx context.Context,
	request *webhookspb.SaveEndpointRequest,
) (*webhookspb.SaveEndpointResponse, error) {
	ctx, req, done, err := s.caller(ctx, webhookspb.WebhooksService_SaveEndpoint_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	// CreatedBy comes off the principal the caller authenticated as. It is
	// written only where the row is new — the store leaves it alone on an update,
	// because an endpoint does not change hands and neither does its provenance.
	endpoint := endpointFromProto(request.GetEndpoint(), request.GetSigningKeys(), req.userID)
	if endpoint == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilEndpointInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "saving a webhook endpoint")

		return nil, err
	}

	req.op.Set(endpointKey, endpoint.ID).
		Set(endpointURLKey, endpoint.URL).
		Set(subscriptionCountKey, len(endpoint.Subscriptions))

	if len(endpoint.Secret.Current) == 0 {
		err = grpcerrors.PrepareAndLogGRPCStatus(webhooks.ErrNoSigningSecret,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument,
			"saving webhook endpoint %q", endpoint.ID)

		return nil, err
	}

	// The read is inside the transaction, and it is the reason
	// webhooks.Store's reads take a database.SQLQueryExecutor rather than a
	// reader: a Tx satisfies that interface, so this one sees the write it
	// follows. On Client.Reader() it would be a read of a database that does not
	// yet contain the row — the endpoint would come back as not found, or, on a
	// re-registration, as the version this call just replaced.
	//
	// It is here because Register fills the endpoint's identity and its live
	// subscriptions but not the timestamps, which the database stamps. Returning
	// the argument instead would answer a save with created_at at the epoch,
	// which is a client rendering "registered in 1970" rather than a client
	// having to ask again.
	saved := endpoint

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if registerErr := s.dispatcher.Register(ctx, tx, req.scope, endpoint); registerErr != nil {
			return registerErr
		}

		read, readErr := s.store.GetEndpoint(ctx, tx, req.scope, endpoint.ID)
		if readErr != nil {
			return readErr
		}

		saved = read

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "saving webhook endpoint %q", endpoint.ID)

		return nil, err
	}

	// Set again rather than only above, because a registration that named no
	// identifier had none to record until Register minted one — and the span of
	// a first registration is the one somebody reads when asking which endpoint
	// this was.
	req.op.Set(endpointKey, saved.ID)

	return &webhookspb.SaveEndpointResponse{Result: EndpointToProto(saved)}, nil
}

// GetEndpoint reads one of the caller's endpoints.
//
// An endpoint in another tenant's scope reads as one that does not exist, which
// is what it is from here — the scope is bound into the statement rather than
// checked in front of it.
func (s *Server) GetEndpoint(
	ctx context.Context,
	request *webhookspb.GetEndpointRequest,
) (*webhookspb.GetEndpointResponse, error) {
	ctx, req, done, err := s.caller(ctx, webhookspb.WebhooksService_GetEndpoint_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetEndpointId()
	req.op.Set(endpointKey, id)

	// The store reads this one secrets included; EndpointToProto is where they
	// stop, because the message it renders into has nowhere to put them.
	endpoint, err := s.store.GetEndpoint(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading webhook endpoint %q", id)

		return nil, err
	}

	return &webhookspb.GetEndpointResponse{Result: EndpointToProto(endpoint)}, nil
}

// ListEndpoints pages the endpoints registered in the caller's tenant.
func (s *Server) ListEndpoints(
	ctx context.Context,
	request *webhookspb.ListEndpointsRequest,
) (*webhookspb.ListEndpointsResponse, error) {
	ctx, req, done, err := s.caller(ctx, webhookspb.WebhooksService_ListEndpoints_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a webhook endpoint page")

		return nil, err
	}

	page, err := s.store.ListEndpoints(ctx, s.client.Reader(), req.scope, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing webhook endpoints")

		return nil, err
	}

	return &webhookspb.ListEndpointsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    EndpointsToProto(page.Data),
	}, nil
}

// ArchiveEndpoint retires one of the caller's endpoints.
//
// It writes through the store rather than the dispatcher, which is the one write
// on this surface that does. There is no Dispatcher.Unregister to call: nothing
// about retiring an endpoint needs a URL checked or a catalog consulted, so the
// gate the other three writes go through has nothing to say about this one.
//
// The endpoint's delivery history is kept, and so are its subscriptions. An
// archived endpoint is excluded from fan-out by its own archived_at, so
// archiving those too would buy nothing and would lose which event types it was
// subscribed to if it is ever re-registered.
//
// An identifier that names nothing in the caller's tenant is answered OK rather
// than NotFound, which is webhooks.Store.ArchiveEndpoint's own decision arriving
// on the wire: an archive that named nothing and an archive of something already
// archived are both the state the caller asked for. It is the right answer here
// as well as there — a NotFound would make this the one method that says whether
// an identifier exists in somebody else's tenant.
func (s *Server) ArchiveEndpoint(
	ctx context.Context,
	request *webhookspb.ArchiveEndpointRequest,
) (*webhookspb.ArchiveEndpointResponse, error) {
	ctx, req, done, err := s.caller(ctx, webhookspb.WebhooksService_ArchiveEndpoint_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetEndpointId()
	req.op.Set(endpointKey, id)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.store.ArchiveEndpoint(ctx, tx, req.scope, id)
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving webhook endpoint %q", id)

		return nil, err
	}

	return &webhookspb.ArchiveEndpointResponse{}, nil
}
