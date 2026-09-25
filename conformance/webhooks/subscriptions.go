package webhooks

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

func subscriptions(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a subscription is added to one of the caller's endpoints", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		offered := catalog(t, caller, 2)
		first, added := offered[0], offered[1]

		saved := registered(t, caller, first)

		sub, err := caller.Surfaces.Webhooks.AddSubscription(caller.Context(t.Context()),
			&webhookspb.AddSubscriptionRequest{EndpointId: saved.GetId(), EventType: added})
		must.NoError(t, err)
		must.NotNil(t, sub.GetResult())

		test.EqOp(t, saved.GetId(), sub.GetResult().GetEndpointId())
		test.EqOp(t, added, sub.GetResult().GetEventType())
		test.NotEq(t, "", sub.GetResult().GetId(), test.Sprint("the subscription has no identity to retire it by"))
		test.Nil(t, sub.GetResult().GetArchivedAt(), test.Sprint("a new subscription came back retired"))

		got := listedSubscriptions(t, caller, saved.GetId())
		test.SliceContains(t, got, sub.GetResult().GetId(), test.Sprint("the added subscription is not listed"))
		test.SliceContains(t, got, subscribedTo(t, saved, first).GetId(),
			test.Sprint("adding a subscription displaced the one the endpoint had"))
	})

	// Idempotent on the pair, which is what makes a retried request safe.
	t.Run("subscribing twice to one event type answers with the same subscription", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		offered := catalog(t, caller, 2)

		saved := registered(t, caller, offered[0])
		ctx := caller.Context(t.Context())
		req := &webhookspb.AddSubscriptionRequest{EndpointId: saved.GetId(), EventType: offered[1]}

		first, err := caller.Surfaces.Webhooks.AddSubscription(ctx, req)
		must.NoError(t, err)

		second, err := caller.Surfaces.Webhooks.AddSubscription(ctx, req)
		must.NoError(t, err)

		test.EqOp(t, first.GetResult().GetId(), second.GetResult().GetId())
	})

	t.Run("a subscription to an event type outside the catalog is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		saved := registered(t, caller, catalog(t, caller, 1)[0])

		_, err := caller.Surfaces.Webhooks.AddSubscription(caller.Context(t.Context()), &webhookspb.AddSubscriptionRequest{
			EndpointId: saved.GetId(),
			EventType:  "conformance.never." + identifiers.New(),
		})
		refused(t, err, codes.InvalidArgument, "a subscription to an uncataloged event type")
	})

	t.Run("a subscription naming no event type is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		saved := registered(t, caller, catalog(t, caller, 1)[0])

		_, err := caller.Surfaces.Webhooks.AddSubscription(caller.Context(t.Context()),
			&webhookspb.AddSubscriptionRequest{EndpointId: saved.GetId()})
		refused(t, err, codes.InvalidArgument, "a subscription to no event type")
	})

	t.Run("a subscription reads back by its identifier", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		eventType := catalog(t, caller, 1)[0]
		saved := registered(t, caller, eventType)
		sub := subscribedTo(t, saved, eventType)

		got := subscription(t, caller, sub.GetId())
		test.EqOp(t, sub.GetId(), got.GetId())
		test.EqOp(t, saved.GetId(), got.GetEndpointId())
		test.EqOp(t, eventType, got.GetEventType())
	})

	t.Run("an endpoint's live subscriptions are listed", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		offered := catalog(t, caller, 2)
		saved := registered(t, caller, offered[0], offered[1])

		page, err := caller.Surfaces.Webhooks.ListSubscriptions(caller.Context(t.Context()),
			&webhookspb.ListSubscriptionsRequest{EndpointId: saved.GetId()})
		must.NoError(t, err)
		test.NotNil(t, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))

		got := subscriptionIDs(page.GetResults())
		test.SliceContains(t, got, subscribedTo(t, saved, offered[0]).GetId())
		test.SliceContains(t, got, subscribedTo(t, saved, offered[1]).GetId())
	})

	// The operation a flat list of event types on the endpoint cannot express:
	// one subscription retired by its own identifier, leaving the endpoint's
	// others alone — and kept rather than deleted, so "when did they stop
	// receiving this" still has an answer.
	t.Run("retiring one subscription leaves the endpoint's others", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		offered := catalog(t, caller, 2)
		saved := registered(t, caller, offered[0], offered[1])

		retired, kept := subscribedTo(t, saved, offered[0]), subscribedTo(t, saved, offered[1])

		_, err := caller.Surfaces.Webhooks.ArchiveSubscription(caller.Context(t.Context()),
			&webhookspb.ArchiveSubscriptionRequest{SubscriptionId: retired.GetId()})
		must.NoError(t, err)

		got := listedSubscriptions(t, caller, saved.GetId())
		test.SliceContains(t, got, kept.GetId(), test.Sprint("retiring one subscription took another with it"))
		test.SliceNotContains(t, got, retired.GetId(), test.Sprint("a retired subscription is still listed as live"))

		test.NotNil(t, subscription(t, caller, retired.GetId()).GetArchivedAt(),
			test.Sprint("a retired subscription reads back live"))
	})

	// The one thing refused outright rather than passed to a statement that
	// would match nothing.
	t.Run("retiring a subscription naming no identifier is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := caller.Surfaces.Webhooks.ArchiveSubscription(caller.Context(t.Context()),
			&webhookspb.ArchiveSubscriptionRequest{})
		refused(t, err, codes.InvalidArgument, "retiring no subscription")
	})
}
