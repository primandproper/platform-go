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

// The subscription half of the surface: four RPCs over the individually
// archivable rows an endpoint's interests are kept as.
//
// They exist as RPCs of their own, rather than as a field of the endpoint a save
// rewrites, for the reason webhooks.Subscription is a row: "stop sending me
// order.created" expressed as a rewrite of the whole set cannot say when it
// happened, has no identifier for the request to name, and races any concurrent
// edit of the same endpoint. It also cannot be permissioned separately, and
// [PermissionAddSubscriptions] beside [PermissionSaveEndpoints] is the reason
// that matters most here.

// AddSubscription subscribes one of the caller's endpoints to one more event
// type.
//
// It writes through webhooks.Dispatcher.Subscribe rather than through the store,
// because Subscribe is the entry point that gates on the consumer's catalog —
// and webhooks.Store.AddSubscription's own documentation says so: an accepted
// subscription to an event type nothing publishes is an endpoint that never
// fires and no signal explaining why.
//
// It is idempotent on the (endpoint, event type) pair, so a retry returns the
// row the first call made rather than a second row for the same pair.
func (s *Server) AddSubscription(
	ctx context.Context,
	request *webhookspb.AddSubscriptionRequest,
) (*webhookspb.AddSubscriptionResponse, error) {
	ctx, req, done, err := s.caller(ctx, webhookspb.WebhooksService_AddSubscription_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	endpointID := request.GetEndpointId()
	eventType := webhooks.EventType(request.GetEventType())

	req.op.Set(endpointKey, endpointID).
		Set(eventTypeKey, eventType.String())

	var subscription *webhooks.Subscription

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		var subErr error
		subscription, subErr = s.dispatcher.Subscribe(ctx, tx, req.scope, endpointID, eventType)

		return subErr
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal,
			"subscribing webhook endpoint %q to %q", endpointID, eventType)

		return nil, err
	}

	return &webhookspb.AddSubscriptionResponse{Result: SubscriptionToProto(subscription)}, nil
}

// GetSubscription reads one of the caller's subscriptions, archived ones
// included — "when did they stop receiving this" is a question about an archived
// row.
//
// A subscription under another tenant's endpoint reads as one that does not
// exist. A subscription carries no scope of its own; its owner is its
// endpoint's, and the read reaches it through that.
func (s *Server) GetSubscription(
	ctx context.Context,
	request *webhookspb.GetSubscriptionRequest,
) (*webhookspb.GetSubscriptionResponse, error) {
	ctx, req, done, err := s.caller(ctx, webhookspb.WebhooksService_GetSubscription_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetSubscriptionId()
	req.op.Set(subscriptionKey, id)

	subscription, err := s.store.GetSubscription(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading webhook subscription %q", id)

		return nil, err
	}

	return &webhookspb.GetSubscriptionResponse{Result: SubscriptionToProto(subscription)}, nil
}

// ListSubscriptions pages the live subscriptions of one of the caller's
// endpoints.
func (s *Server) ListSubscriptions(
	ctx context.Context,
	request *webhookspb.ListSubscriptionsRequest,
) (*webhookspb.ListSubscriptionsResponse, error) {
	ctx, req, done, err := s.caller(ctx, webhookspb.WebhooksService_ListSubscriptions_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	endpointID := request.GetEndpointId()
	req.op.Set(endpointKey, endpointID)

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument,
			"reading the filter of a webhook subscription page")

		return nil, err
	}

	page, err := s.store.ListSubscriptions(ctx, s.client.Reader(), req.scope, endpointID, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal,
			"listing the subscriptions of webhook endpoint %q", endpointID)

		return nil, err
	}

	return &webhookspb.ListSubscriptionsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    SubscriptionsToProto(page.Data),
	}, nil
}

// ArchiveSubscription retires one of the caller's subscriptions, so the endpoint
// stops receiving that event type without its other subscriptions, its delivery
// history, or its identity being touched.
//
// It writes through webhooks.Dispatcher.Unsubscribe, which is the dispatcher's
// name for the store's ArchiveSubscription.
func (s *Server) ArchiveSubscription(
	ctx context.Context,
	request *webhookspb.ArchiveSubscriptionRequest,
) (*webhookspb.ArchiveSubscriptionResponse, error) {
	ctx, req, done, err := s.caller(ctx, webhookspb.WebhooksService_ArchiveSubscription_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetSubscriptionId()
	req.op.Set(subscriptionKey, id)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.dispatcher.Unsubscribe(ctx, tx, req.scope, id)
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving webhook subscription %q", id)

		return nil, err
	}

	return &webhookspb.ArchiveSubscriptionResponse{}, nil
}
