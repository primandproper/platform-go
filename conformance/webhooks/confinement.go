package webhooks

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func confinement(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("the endpoints listing reaches the caller's tenant only", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		eventType := catalog(t, mine, 1)[0]

		ours := registered(t, mine, eventType)
		neighbor := registered(t, theirs, eventType)

		// Presence and absence of two named endpoints, never a count: this
		// listing may run against a database the suite does not own.
		got := listedEndpoints(t, mine)
		test.SliceContains(t, got, ours.GetId(), test.Sprint("this tenant's own endpoint was missing from its listing"))
		test.SliceNotContains(t, got, neighbor.GetId(), test.Sprint("a neighboring tenant's endpoint reached this listing"))

		// The mirror image, which rules out a rule favoring whichever caller
		// was made first.
		got = listedEndpoints(t, theirs)
		test.SliceContains(t, got, neighbor.GetId())
		test.SliceNotContains(t, got, ours.GetId())
	})

	// Asked with real identifiers from the wrong tenant. The subscriptions
	// table has no scope of its own and reaches one through its endpoint, so
	// the subscription calls are as much a test of that join as of the scope.
	t.Run("a neighbor's endpoint and its subscriptions are absent to every call that names them", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		offered := catalog(t, mine, 2)
		held, other := offered[0], offered[1]

		ours := registered(t, mine, held)
		sub := subscribedTo(t, ours, held)

		// The positive control. Every absence below is also what a surface
		// reaching nothing at all would answer.
		test.EqOp(t, ours.GetId(), endpoint(t, mine, ours.GetId()).GetId())
		test.EqOp(t, sub.GetId(), subscription(t, mine, sub.GetId()).GetId())
		test.SliceContains(t, listedSubscriptions(t, mine, ours.GetId()), sub.GetId())

		ctx := theirs.Context(t.Context())

		_, err := theirs.Surfaces.Webhooks.GetEndpoint(ctx, &webhookspb.GetEndpointRequest{EndpointId: ours.GetId()})
		refused(t, err, codes.NotFound, "reading a neighbor's endpoint")

		_, err = theirs.Surfaces.Webhooks.RotateSecret(ctx,
			&webhookspb.RotateSecretRequest{EndpointId: ours.GetId(), SigningKey: rolledKey()})
		refused(t, err, codes.NotFound, "rotating a neighbor's endpoint")

		_, err = theirs.Surfaces.Webhooks.AddSubscription(ctx,
			&webhookspb.AddSubscriptionRequest{EndpointId: ours.GetId(), EventType: other})
		refused(t, err, codes.NotFound, "subscribing a neighbor's endpoint")

		_, err = theirs.Surfaces.Webhooks.GetSubscription(ctx, &webhookspb.GetSubscriptionRequest{SubscriptionId: sub.GetId()})
		refused(t, err, codes.NotFound, "reading a neighbor's subscription")

		// The listing is a page, and a neighbor's endpoint reads as one with
		// nothing on it rather than as a refusal.
		test.SliceNotContains(t, listedSubscriptions(t, theirs, ours.GetId()), sub.GetId(),
			test.Sprint("a neighboring tenant listed this endpoint's subscriptions"))

		// And the refused subscription was not written: the endpoint still
		// hears about only what it asked to.
		test.SliceNotContains(t, subscribedEventTypes(endpoint(t, mine, ours.GetId())), other,
			test.Sprint("a neighbor's refused subscription reached the endpoint"))
	})

	// Archiving somebody else's endpoint or subscription answers OK, and both
	// halves of that are deliberate. The statement binds the caller's scope, so
	// it matches nothing and the row is untouched — the half that matters, and
	// the only one a status code cannot show. The OK is because an archive of
	// nothing and an archive of something already archived both leave the
	// thing not live, which is what was asked for; answering NotFound would
	// make this the one call that says whether an identifier exists in another
	// tenant.
	t.Run("a neighbor's archive touches neither the endpoint nor its subscriptions", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		eventType := catalog(t, mine, 1)[0]

		ours := registered(t, mine, eventType)
		sub := subscribedTo(t, ours, eventType)

		ctx := theirs.Context(t.Context())

		_, err := theirs.Surfaces.Webhooks.ArchiveEndpoint(ctx, &webhookspb.ArchiveEndpointRequest{EndpointId: ours.GetId()})
		must.NoError(t, err, must.Sprint("a neighbor's archive was answered differently from an archive of nothing"))

		_, err = theirs.Surfaces.Webhooks.ArchiveSubscription(ctx,
			&webhookspb.ArchiveSubscriptionRequest{SubscriptionId: sub.GetId()})
		must.NoError(t, err, must.Sprint("a neighbor's archive was answered differently from an archive of nothing"))

		// Read back as the owner, which is also the positive control: the rows
		// are reachable, and live.
		test.Nil(t, endpoint(t, mine, ours.GetId()).GetArchivedAt(),
			test.Sprint("a neighboring tenant retired this endpoint"))
		test.Nil(t, subscription(t, mine, sub.GetId()).GetArchivedAt(),
			test.Sprint("a neighboring tenant retired this endpoint's subscription"))
		test.SliceContains(t, listedEndpoints(t, mine), ours.GetId())
		test.SliceContains(t, listedSubscriptions(t, mine, ours.GetId()), sub.GetId())
	})

	// An endpoint does not change hands. Its identifier is the caller's to
	// choose, so a collision is theirs to hear about — and what they hear is
	// that the identifier is not available, never that somebody else holds it.
	t.Run("an endpoint identifier another tenant holds is not available, and the refusal does not say why", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)
		eventType := catalog(t, mine, 1)[0]

		neighbor := registered(t, theirs, eventType)

		// The positive control: the caller may save an endpoint by an
		// identifier it holds, so the refusal below is about whose it is.
		ours := registered(t, mine, eventType)
		again := endpointFor(eventType)
		again.Id = ours.GetId()
		_, err := save(t, mine, again, keyring())
		must.NoError(t, err, must.Sprint("the caller cannot save its own endpoint again; the refusal below proves nothing"))

		taken := endpointFor(eventType)
		taken.Id = neighbor.GetId()

		_, err = save(t, mine, taken, keyring())
		refused(t, err, codes.AlreadyExists, "saving an endpoint by a neighbor's identifier")
		test.StrNotContains(t, status.Convert(err).Message(), "another scope",
			test.Sprint("the refusal told the caller another tenant holds the identifier"))

		// And the neighbor's endpoint is still theirs, as they registered it.
		got := endpoint(t, theirs, neighbor.GetId())
		test.EqOp(t, neighbor.GetName(), got.GetName())
		test.Nil(t, got.GetArchivedAt())
	})
}
