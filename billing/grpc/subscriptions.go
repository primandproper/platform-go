package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/billing/billingpb"

	"github.com/primandproper/primitives-go/database"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The recurring half: who is paying for what, and until when.
//
// Four reads and one administrative write, and no way to open, sync or move a
// subscription. billing.SubscriptionStore has three writes this service does not
// serve — CreateSubscription, UpdateSubscription and SetSubscriptionStatus — and
// their caller is a processor callback already inside the consumer's own
// transaction. See this package's documentation for the ruling and
// billing.Store for it stated on each method.
//
// The reads divide by who they answer to. ListSubscriptions is scope-wide and is
// the operator's, behind a grant of its own. The other three are somebody's own
// and pass through [AccountAuthorizer]: two name the account in the request, and
// GetSubscription reads the row first and then asks about the account on it.

// GetSubscription reads one live subscription.
//
// The permission says the caller may make this kind of read and the authorizer
// says whether this row is theirs, and it is asked after the read because the
// account is on the row rather than in the request. A caller the authorizer
// refuses is told the same thing a caller naming a subscription that does not
// exist is told, so an id nobody holds and an id somebody else holds are one
// answer — see authorizeRowOwner.
func (s *Server) GetSubscription(
	ctx context.Context,
	request *billingpb.GetSubscriptionRequest,
) (*billingpb.GetSubscriptionResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_GetSubscription_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetSubscriptionId()
	req.op.Set(subscriptionKey, id)

	subscription, err := s.store.GetSubscription(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading subscription %q", id)

		return nil, err
	}

	req.op.Set(accountKey, subscription.BelongsToAccount)

	if err = s.authorizeRowOwner(ctx, req, subscription.BelongsToAccount,
		"reading subscription %q", id); err != nil {
		return nil, err
	}

	return &billingpb.GetSubscriptionResponse{Result: SubscriptionToProto(subscription)}, nil
}

// ListSubscriptions pages every subscription in the caller's scope.
//
// It is the operator's read and answers to no account, which is why it carries
// PermissionListAllSubscriptions rather than PermissionReadSubscriptions and why
// no authorizer is consulted: there is no account named to ask about, and the
// grant is the whole answer.
func (s *Server) ListSubscriptions(
	ctx context.Context,
	request *billingpb.ListSubscriptionsRequest,
) (*billingpb.ListSubscriptionsResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ListSubscriptions_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a subscription page")

		return nil, err
	}

	page, err := s.store.ListSubscriptions(ctx, s.client.Reader(), req.scope, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing subscriptions")

		return nil, err
	}

	return &billingpb.ListSubscriptionsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    SubscriptionsToProto(page.Data),
	}, nil
}

// ListSubscriptionsForAccount pages one account's subscriptions, current and
// lapsed alike.
func (s *Server) ListSubscriptionsForAccount(
	ctx context.Context,
	request *billingpb.ListSubscriptionsForAccountRequest,
) (*billingpb.ListSubscriptionsForAccountResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ListSubscriptionsForAccount_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	accountID := request.GetAccountId()
	req.op.Set(accountKey, accountID)

	// The filter is read before the authorizer is asked, so a malformed request
	// is answered as malformed whoever sent it — saying so discloses nothing
	// about any account. Everything past this point is gated.
	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument,
			"reading the filter of a subscription page")

		return nil, err
	}

	if err = s.requireAccount(req, accountID, "listing an account's subscriptions"); err != nil {
		return nil, err
	}

	if err = s.authorizeNamedAccount(ctx, req, accountID,
		"authorizing the caller against account %q", accountID); err != nil {
		return nil, err
	}

	page, err := s.store.ListSubscriptionsForAccount(ctx, s.client.Reader(), req.scope, accountID, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing subscriptions for account %q", accountID)

		return nil, err
	}

	return &billingpb.ListSubscriptionsForAccountResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    SubscriptionsToProto(page.Data),
	}, nil
}

// ListCurrentSubscriptions pages the account's subscriptions whose paid period
// covers now.
//
// It is the read an entitlement screen makes, and it deliberately does not
// filter on the status. Which reported status leaves an account entitled is
// policy, it differs between deployments selling the same thing, and this
// answers the part that is a fact about the row — the caller reads
// Subscription.status off what comes back and decides. That is the same division
// billing/plans makes in process, and the reason there is no standing on any
// message in this schema.
func (s *Server) ListCurrentSubscriptions(
	ctx context.Context,
	request *billingpb.ListCurrentSubscriptionsRequest,
) (*billingpb.ListCurrentSubscriptionsResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ListCurrentSubscriptions_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	accountID := request.GetAccountId()
	req.op.Set(accountKey, accountID)

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument,
			"reading the filter of a current subscription page")

		return nil, err
	}

	if err = s.requireAccount(req, accountID, "listing an account's current subscriptions"); err != nil {
		return nil, err
	}

	if err = s.authorizeNamedAccount(ctx, req, accountID,
		"authorizing the caller against account %q", accountID); err != nil {
		return nil, err
	}

	page, err := s.store.ListCurrentSubscriptions(ctx, s.client.Reader(), req.scope, accountID, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal,
			"listing current subscriptions for account %q", accountID)

		return nil, err
	}

	return &billingpb.ListCurrentSubscriptionsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    SubscriptionsToProto(page.Data),
	}, nil
}

// ArchiveSubscription retires a subscription administratively.
//
// It is not a cancellation and must not be used as one: a cancelled subscription
// is one whose status says so, which is a fact the provider reports, and
// archiving hides the row from every read that does not ask for archived rows
// while changing nothing about what it holds. The ledger rows pointing at it are
// left alone.
//
// It is behind an administrative grant and asks no authorizer, which is the
// deliberate consequence of the sentence above: this is not something an account
// does to its own subscription, because the thing an account wants — to stop
// paying — is a call to the payment provider and arrives here as a status.
func (s *Server) ArchiveSubscription(
	ctx context.Context,
	request *billingpb.ArchiveSubscriptionRequest,
) (*billingpb.ArchiveSubscriptionResponse, error) {
	ctx, req, done, err := s.caller(ctx, billingpb.BillingService_ArchiveSubscription_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetSubscriptionId()
	req.op.Set(subscriptionKey, id)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.store.ArchiveSubscription(ctx, tx, req.scope, id)
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving subscription %q", id)

		return nil, err
	}

	return &billingpb.ArchiveSubscriptionResponse{}, nil
}
