package webhooks

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	domain "github.com/primandproper/platform-go/v14/webhooks"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

func endpoints(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("an endpoint is answered as it was stored and reads back the same", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		eventType := catalog(t, caller, 1)[0]

		input := endpointFor(s, eventType)
		saved := register(t, caller, input, keyring())

		test.NotEq(t, "", saved.GetId(), test.Sprint("the store minted no identifier"))
		test.EqOp(t, input.GetName(), saved.GetName())
		test.EqOp(t, deliveryURL(s), saved.GetUrl())

		// The default a registration settles on its way through the dispatcher,
		// which is how a client can tell the write went through it at all.
		test.EqOp(t, domain.DefaultContentType, saved.GetContentType())

		// The creation time is the database's, which is the reason the answer is
		// read back rather than echoed: the request carried none.
		must.NotNil(t, saved.GetCreatedAt())
		test.False(t, saved.GetCreatedAt().AsTime().IsZero(),
			test.Sprint("created_at came back at the epoch, so the answer is the request read out again"))
		test.Nil(t, saved.GetArchivedAt(), test.Sprint("a new endpoint came back retired"))

		// The subscription set came back as rows with identities, which is what
		// makes one of them retirable at all.
		sub := subscribedTo(t, saved, eventType)
		test.NotEq(t, "", sub.GetId(), test.Sprint("the subscription has no identity to retire it by"))
		test.EqOp(t, saved.GetId(), sub.GetEndpointId())

		got := endpoint(t, caller, saved.GetId())
		test.EqOp(t, saved.GetId(), got.GetId())
		test.EqOp(t, input.GetName(), got.GetName())
		test.EqOp(t, deliveryURL(s), got.GetUrl())
		test.SliceContains(t, subscriptionIDs(got.GetSubscriptions()), sub.GetId())
	})

	// The provenance comes off the connection. A request has nowhere to put
	// one, which is the same reading the OAuth2 client registry takes of an
	// owner.
	t.Run("the caller is recorded as the endpoint's registrant", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsUser(t, caller)

		saved := registered(t, s, caller, catalog(t, caller, 1)[0])
		test.EqOp(t, caller.UserID, saved.GetCreatedBy())
		test.EqOp(t, caller.UserID, endpoint(t, caller, saved.GetId()).GetCreatedBy())
	})

	// A save is a whole registration, keys included, and this refusal is what
	// keeps leaving them out from being a silent rotation to nothing.
	t.Run("a registration naming no signing key is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := save(t, caller, endpointFor(s, catalog(t, caller, 1)[0]), nil)
		refused(t, err, codes.InvalidArgument, "a registration with no signing key")
	})

	t.Run("a registration naming no endpoint is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := save(t, caller, nil, keyring())
		refused(t, err, codes.InvalidArgument, "a registration with no endpoint")
	})

	// The catalog gate. An accepted subscription to an event type nothing
	// publishes is an endpoint that never fires, with no signal saying why.
	t.Run("an endpoint subscribing to an event type outside the catalog is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := save(t, caller, endpointFor(s, "conformance.never."+identifiers.New()), keyring())
		refused(t, err, codes.InvalidArgument, "a subscription to an uncataloged event type")
	})

	t.Run("an endpoint subscribing to nothing is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := save(t, caller, endpointFor(s), keyring())
		refused(t, err, codes.InvalidArgument, "an endpoint with no subscriptions")
	})

	// An endpoint is a URL somebody typed that this deployment then makes
	// signed requests to, and the cloud instance metadata address is the
	// textbook thing to type. Whatever a deployment's URL check is — this
	// module's default or an allowlist of its own hosts — that address is not
	// on it.
	t.Run("an endpoint aimed at the cloud instance metadata address is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		eventType := catalog(t, caller, 1)[0]

		// The positive control: the same registration aimed at the
		// subject's delivery address is accepted, so the refusal below is about the
		// address rather than about everything.
		registered(t, s, caller, eventType)

		input := endpointFor(s, eventType)
		input.Url = "https://169.254.169.254/latest/meta-data/"

		_, err := save(t, caller, input, keyring())
		refused(t, err, codes.InvalidArgument, "an endpoint aimed at the metadata address")
	})

	// An endpoint the caller already has keeps its identity when it is saved
	// again, and its subscription set is reconciled rather than replaced — the
	// subscription both saves name is the same row before and after.
	t.Run("saving an endpoint again keeps its identity and reconciles its subscriptions", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		offered := catalog(t, caller, 2)
		dropped, kept := offered[0], offered[1]

		saved := registered(t, s, caller, dropped, kept)
		keptID := subscribedTo(t, saved, kept).GetId()

		input := endpointFor(s, kept)
		input.Id = saved.GetId()

		resaved, err := save(t, caller, input, keyring())
		must.NoError(t, err)

		test.EqOp(t, saved.GetId(), resaved.GetId())
		test.SliceContains(t, subscribedEventTypes(resaved), kept)
		test.SliceNotContains(t, subscribedEventTypes(resaved), dropped,
			test.Sprint("a re-registration left a subscription it no longer names"))
		test.EqOp(t, keptID, subscribedTo(t, resaved, kept).GetId(),
			test.Sprint("a re-registration replaced a subscription it kept"))

		test.SliceContains(t, listedSubscriptions(t, caller, saved.GetId()), keptID)
	})

	// Retired rather than deleted: the attempts log outlives the endpoint,
	// because "what did we send them" is asked most often after somebody has
	// been removed.
	t.Run("an archived endpoint is kept, marked retired, and out of the listing", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		saved := registered(t, s, caller, catalog(t, caller, 1)[0])

		// The positive control: listed while live, so its absence afterwards is
		// the archive's doing.
		test.SliceContains(t, listedEndpoints(t, caller), saved.GetId())

		_, err := caller.Surfaces.Webhooks.ArchiveEndpoint(caller.Context(t.Context()),
			&webhookspb.ArchiveEndpointRequest{EndpointId: saved.GetId()})
		must.NoError(t, err)

		test.NotNil(t, endpoint(t, caller, saved.GetId()).GetArchivedAt(),
			test.Sprint("an archived endpoint reads back live"))
		test.SliceNotContains(t, listedEndpoints(t, caller), saved.GetId(),
			test.Sprint("an archived endpoint is still listed"))
	})

	// The granted half of include_archived. An administrator holds the grant
	// that retires an endpoint, which is the grant a deployment reads the
	// archive off, so asking is answered with the retired row.
	t.Run("an administrator asking for retired endpoints receives them", func(t *testing.T) {
		t.Parallel()

		admin := s.Subject(t, conformance.AsAdmin())
		eventType := catalog(t, admin, 1)[0]
		live := registered(t, admin, eventType)
		retired := registered(t, admin, eventType)

		ctx := admin.Context(t.Context())

		_, err := admin.Surfaces.Webhooks.ArchiveEndpoint(ctx,
			&webhookspb.ArchiveEndpointRequest{EndpointId: retired.GetId()})
		must.NoError(t, err)

		// The control: without asking, the retired endpoint is not listed.
		test.SliceNotContains(t, listedEndpoints(t, admin), retired.GetId())

		include := true

		page, err := admin.Surfaces.Webhooks.ListEndpoints(ctx,
			&webhookspb.ListEndpointsRequest{Filter: &filteringpb.QueryFilter{IncludeArchived: &include}})
		must.NoError(t, err)

		ids := make([]string, 0, len(page.GetResults()))
		for _, e := range page.GetResults() {
			ids = append(ids, e.GetId())
		}

		test.SliceContains(t, ids, live.GetId())
		test.SliceContains(t, ids, retired.GetId(),
			test.Sprint("an administrator asked for retired endpoints and was answered without them"))
	})

	// Archiving answers "this endpoint is not live", which is true of an
	// identifier that names nothing. The confinement suite relies on this
	// being the same answer a neighbor's endpoint gets.
	t.Run("archiving an identifier that names nothing succeeds", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := caller.Surfaces.Webhooks.ArchiveEndpoint(caller.Context(t.Context()),
			&webhookspb.ArchiveEndpointRequest{EndpointId: absentID()})
		test.NoError(t, err)
	})
}
