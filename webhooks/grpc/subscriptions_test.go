package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/webhooks"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAddSubscription(T *testing.T) {
	T.Parallel()

	T.Run("subscribes one of the caller's endpoints", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope, orderCreated)

		res, err := h.server.AddSubscription(h.ctx(t), &webhookspb.AddSubscriptionRequest{
			EndpointId: seeded.ID,
			EventType:  string(orderShipped),
		})
		must.NoError(t, err)
		must.NotNil(t, res.GetResult())

		test.EqOp(t, seeded.ID, res.GetResult().GetEndpointId())
		test.EqOp(t, string(orderShipped), res.GetResult().GetEventType())
		test.NotEq(t, "", res.GetResult().GetId(), test.Sprint("the subscription has no identity to archive it by"))
		test.Nil(t, res.GetResult().GetArchivedAt())
	})

	// The catalog gate, which is why this RPC goes through the dispatcher rather
	// than through the store: an accepted subscription to an event type nothing
	// publishes is an endpoint that never fires and no signal explaining why.
	T.Run("refuses an event type outside the catalog", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope)

		_, err := h.server.AddSubscription(h.ctx(t), &webhookspb.AddSubscriptionRequest{
			EndpointId: seeded.ID,
			EventType:  string(uncataloged),
		})
		must.Error(t, err)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.ErrorIs(t, err, webhooks.ErrUnknownEventType)
	})

	T.Run("refuses an empty event type", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope)

		_, err := h.server.AddSubscription(h.ctx(t), &webhookspb.AddSubscriptionRequest{
			EndpointId: seeded.ID,
		})
		must.Error(t, err)

		// The platform mapper answers this one, because the sentinel wraps
		// errors.ErrEmptyInputParameter — which is the roster's "platform" row
		// arriving on a wire.
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	// Idempotent on the pair, which is what makes a retried request safe.
	T.Run("subscribing twice returns the same row", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		ctx := h.ctx(t)
		seeded := h.seed(t, testScope, orderCreated)

		first, err := h.server.AddSubscription(ctx, &webhookspb.AddSubscriptionRequest{
			EndpointId: seeded.ID,
			EventType:  string(orderShipped),
		})
		must.NoError(t, err)

		second, err := h.server.AddSubscription(ctx, &webhookspb.AddSubscriptionRequest{
			EndpointId: seeded.ID,
			EventType:  string(orderShipped),
		})
		must.NoError(t, err)

		test.EqOp(t, first.GetResult().GetId(), second.GetResult().GetId())
	})

	// The subscriptions table has no scope of its own, so the write reaches one
	// through the endpoint — and an endpoint in another tenant is not there.
	T.Run("cannot subscribe another tenant's endpoint", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope)

		_, err := h.server.AddSubscription(h.otherCtx(t), &webhookspb.AddSubscriptionRequest{
			EndpointId: seeded.ID,
			EventType:  string(orderShipped),
		})
		must.Error(t, err)

		test.EqOp(t, codes.NotFound, status.Code(err))

		// Nothing was written, which is the half the status code does not say.
		page, err := h.store.ListSubscriptions(h.ctx(t), h.db.Reader(), testScope, seeded.ID, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, page.Data)
	})

	T.Run("refuses a caller with no principal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.AddSubscription(t.Context(), &webhookspb.AddSubscriptionRequest{})
		must.Error(t, err)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestGetSubscription(T *testing.T) {
	T.Parallel()

	T.Run("reads one of the caller's subscriptions", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		ctx := h.ctx(t)
		seeded := h.seed(t, testScope)

		page, err := h.store.ListSubscriptions(ctx, h.db.Reader(), testScope, seeded.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)

		res, err := h.server.GetSubscription(ctx, &webhookspb.GetSubscriptionRequest{
			SubscriptionId: page.Data[0].ID,
		})
		must.NoError(t, err)

		test.EqOp(t, page.Data[0].ID, res.GetResult().GetId())
		test.EqOp(t, string(orderCreated), res.GetResult().GetEventType())
	})

	T.Run("cannot read a subscription under another tenant's endpoint", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope)

		page, err := h.store.ListSubscriptions(h.ctx(t), h.db.Reader(), testScope, seeded.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)

		_, err = h.server.GetSubscription(h.otherCtx(t), &webhookspb.GetSubscriptionRequest{
			SubscriptionId: page.Data[0].ID,
		})
		must.Error(t, err)

		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("refuses a caller with no principal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.GetSubscription(t.Context(), &webhookspb.GetSubscriptionRequest{})
		must.Error(t, err)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestListSubscriptions(T *testing.T) {
	T.Parallel()

	T.Run("pages an endpoint's live subscriptions", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope, orderCreated, orderShipped)

		res, err := h.server.ListSubscriptions(h.ctx(t), &webhookspb.ListSubscriptionsRequest{
			EndpointId: seeded.ID,
		})
		must.NoError(t, err)

		test.SliceLen(t, 2, res.GetResults())
		test.NotNil(t, res.GetPagination())
	})

	T.Run("another tenant sees none of them", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope, orderCreated, orderShipped)

		res, err := h.server.ListSubscriptions(h.otherCtx(t), &webhookspb.ListSubscriptionsRequest{
			EndpointId: seeded.ID,
		})
		must.NoError(t, err)

		test.SliceEmpty(t, res.GetResults())
	})

	T.Run("refuses a caller with no principal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.ListSubscriptions(t.Context(), &webhookspb.ListSubscriptionsRequest{})
		must.Error(t, err)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestArchiveSubscription(T *testing.T) {
	T.Parallel()

	// The operation a flat list of event types on the endpoint cannot express:
	// one subscription retired, by its own identifier, leaving the endpoint's
	// others and its history alone.
	T.Run("retires one subscription and leaves the rest", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		ctx := h.ctx(t)
		seeded := h.seed(t, testScope, orderCreated, orderShipped)

		page, err := h.store.ListSubscriptions(ctx, h.db.Reader(), testScope, seeded.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 2, page.Data)

		retired := page.Data[0]

		_, err = h.server.ArchiveSubscription(ctx, &webhookspb.ArchiveSubscriptionRequest{
			SubscriptionId: retired.ID,
		})
		must.NoError(t, err)

		remaining, err := h.server.ListSubscriptions(ctx, &webhookspb.ListSubscriptionsRequest{
			EndpointId: seeded.ID,
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, remaining.GetResults())
		test.NotEq(t, retired.ID, remaining.GetResults()[0].GetId())

		// Archived rather than deleted, so "when did they stop receiving this"
		// still has an answer.
		read, err := h.server.GetSubscription(ctx, &webhookspb.GetSubscriptionRequest{
			SubscriptionId: retired.ID,
		})
		must.NoError(t, err)
		test.NotNil(t, read.GetResult().GetArchivedAt())
	})

	// Another tenant's archive touches nothing and answers OK, which is the same
	// pair of decisions ArchiveEndpoint makes and is made for the same reason:
	// the statement binds the caller's scope so the row is untouched, and a
	// NotFound would make this the one method that says whether an identifier
	// exists somewhere else.
	T.Run("another tenant's archive touches nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope)

		page, err := h.store.ListSubscriptions(h.ctx(t), h.db.Reader(), testScope, seeded.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)

		_, err = h.server.ArchiveSubscription(h.otherCtx(t), &webhookspb.ArchiveSubscriptionRequest{
			SubscriptionId: page.Data[0].ID,
		})
		must.NoError(t, err)

		still, err := h.store.GetSubscription(h.ctx(t), h.db.Reader(), testScope, page.Data[0].ID)
		must.NoError(t, err)
		test.Nil(t, still.ArchivedAt)
	})

	// An empty identifier is the one thing the dispatcher refuses outright,
	// rather than passing to a statement that would match nothing.
	T.Run("refuses an empty identifier", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.ArchiveSubscription(h.ctx(t), &webhookspb.ArchiveSubscriptionRequest{})
		must.Error(t, err)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.True(t, platformerrors.Is(err, platformerrors.ErrInvalidIDProvided))
	})

	T.Run("refuses a caller with no principal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.ArchiveSubscription(t.Context(), &webhookspb.ArchiveSubscriptionRequest{})
		must.Error(t, err)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}
