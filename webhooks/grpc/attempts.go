package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// ListAttempts pages the attempts recorded for one of the caller's deliveries:
// what was tried, when, and what came back.
//
// It is the one read on this surface that is not about configuration, and it is
// the other half of what an endpoint management screen shows — did it get
// through, and what did the subscriber say. [PermissionReadAttempts] is its own
// grant for that reason.
//
// A delivery in another tenant's scope reads as one with no attempts, which is
// what it is from here. That is a quieter answer than the not-found the endpoint
// reads give, and it is the store's: an attempts page is a page, and an empty
// one is a well-formed answer rather than a refusal.
//
// It is the only method of the delivery pipeline on this surface. The seven the
// worker drives, EndpointsForEvent and Enqueue are absent, and webhooks.Store
// says why on each — Enqueue's absence being the one that matters most, since
// it is the only consumer-facing method here that must never be reachable over a
// wire.
func (s *Server) ListAttempts(
	ctx context.Context,
	request *webhookspb.ListAttemptsRequest,
) (*webhookspb.ListAttemptsResponse, error) {
	ctx, req, done, err := s.caller(ctx, webhookspb.WebhooksService_ListAttempts_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	deliveryID := request.GetDeliveryId()
	req.op.Set(deliveryKey, deliveryID)

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument,
			"reading the filter of a webhook attempt page")

		return nil, err
	}

	page, err := s.store.ListAttempts(ctx, s.client.Reader(), req.scope, deliveryID, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal,
			"listing the attempts of webhook delivery %q", deliveryID)

		return nil, err
	}

	return &webhookspb.ListAttemptsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    AttemptsToProto(page.Data),
	}, nil
}
