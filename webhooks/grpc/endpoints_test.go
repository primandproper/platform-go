package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/webhooks"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSaveEndpoint(T *testing.T) {
	T.Parallel()

	T.Run("registers an endpoint and answers with the saved row", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.SaveEndpoint(h.ctx(t), &webhookspb.SaveEndpointRequest{
			Endpoint:    endpointInput(),
			SigningKeys: testKeys(),
		})
		must.NoError(t, err)
		must.NotNil(t, res.GetResult())

		saved := res.GetResult()
		test.NotEq(t, "", saved.GetId(), test.Sprint("the store minted no identifier"))
		test.EqOp(t, testURL, saved.GetUrl())

		// The default the endpoint's own EnsureDefaults fills, which is a sign
		// the write went through the dispatcher rather than the store.
		test.EqOp(t, webhooks.DefaultContentType, saved.GetContentType())

		// The timestamp is the database's, and it is the reason the handler reads
		// the endpoint back inside its own transaction: the argument Register
		// filled carries no created_at at all.
		must.NotNil(t, saved.GetCreatedAt())
		test.False(t, saved.GetCreatedAt().AsTime().IsZero(),
			test.Sprint("created_at came back at the epoch, so the response is the request read out again"))

		// The subscription set came back as rows with identities, which is what
		// makes ArchiveSubscription callable at all.
		must.SliceLen(t, 1, saved.GetSubscriptions())
		test.EqOp(t, string(orderCreated), saved.GetSubscriptions()[0].GetEventType())
		test.NotEq(t, "", saved.GetSubscriptions()[0].GetId())
	})

	// The property the issue this package was written for names first: whatever
	// SaveEndpoint stores for signing must not be readable back over the surface.
	//
	// It is asserted on the message rather than on a value, because the guarantee
	// is structural: WebhookEndpoint has no field for a keyring, so there is no
	// assignment for somebody to add later. What this pins is that the keys did
	// reach the database — otherwise the test would pass over an endpoint that
	// was never signed for anything.
	T.Run("does not hand the signing keys back", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		ctx := h.ctx(t)

		keys := &webhookspb.WebhookSigningKeys{
			Current:  []byte("current-key"),
			Previous: []byte("previous-key"),
		}

		res, err := h.server.SaveEndpoint(ctx, &webhookspb.SaveEndpointRequest{
			Endpoint:    endpointInput(),
			SigningKeys: keys,
		})
		must.NoError(t, err)

		id := res.GetResult().GetId()

		// The store reads an endpoint secrets included, which is what makes the
		// converter the only thing standing between the keyring and a client.
		stored, err := h.store.GetEndpoint(ctx, h.db.Reader(), testScope, id)
		must.NoError(t, err)
		test.Eq(t, keys.GetCurrent(), stored.Secret.Current)
		test.Eq(t, keys.GetPrevious(), stored.Secret.Previous)

		// And no read on this surface renders them. Encoding the message is the
		// broadest form of the assertion available: a field added later would
		// show up in the bytes.
		read, err := h.server.GetEndpoint(ctx, &webhookspb.GetEndpointRequest{EndpointId: id})
		must.NoError(t, err)

		test.False(t, messageCarries(t, read.GetResult(), keys.GetCurrent()),
			test.Sprint("the current signing key is readable through GetEndpoint"))
		test.False(t, messageCarries(t, read.GetResult(), keys.GetPrevious()),
			test.Sprint("the previous signing key is readable through GetEndpoint"))
	})

	// The keys are copied out of the request rather than aliased, because the
	// request message is the unmarshaled wire buffer and its lifetime is the
	// RPC's, while the keyring travels into a store write and into a consumer's
	// hooks.
	T.Run("copies the keyring out of the request", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		ctx := h.ctx(t)

		current := []byte("current-key")
		keys := &webhookspb.WebhookSigningKeys{Current: current}

		res, err := h.server.SaveEndpoint(ctx, &webhookspb.SaveEndpointRequest{
			Endpoint:    endpointInput(),
			SigningKeys: keys,
		})
		must.NoError(t, err)

		// What the caller does to their own buffer afterwards.
		for i := range current {
			current[i] = 0
		}

		stored, err := h.store.GetEndpoint(ctx, h.db.Reader(), testScope, res.GetResult().GetId())
		must.NoError(t, err)
		test.Eq(t, []byte("current-key"), stored.Secret.Current)
	})

	// A save is a full re-registration, keys included, and this is the refusal
	// that keeps the omission from being a silent rotation to nothing.
	T.Run("refuses a save that names no signing key", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.SaveEndpoint(h.ctx(t), &webhookspb.SaveEndpointRequest{
			Endpoint: endpointInput(),
		})
		must.Error(t, err)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.ErrorIs(t, err, webhooks.ErrNoSigningSecret)
	})

	T.Run("refuses a save that names no endpoint", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.SaveEndpoint(h.ctx(t), &webhookspb.SaveEndpointRequest{
			SigningKeys: testKeys(),
		})
		must.Error(t, err)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.True(t, platformerrors.Is(err, platformerrors.ErrNilInputParameter))
	})

	// The provenance comes off the connection. A request has nowhere to put one,
	// which is the same reading the OAuth2 client registry takes of an owner.
	T.Run("stamps the caller as the registrant", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.SaveEndpoint(h.ctx(t), &webhookspb.SaveEndpointRequest{
			Endpoint:    endpointInput(),
			SigningKeys: testKeys(),
		})
		must.NoError(t, err)

		test.EqOp(t, testUser, res.GetResult().GetCreatedBy())
	})

	// The catalog gate, reached through Register rather than through the store.
	T.Run("refuses an event type outside the catalog", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		input := endpointInput()
		input.EventTypes = []string{string(uncataloged)}

		_, err := h.server.SaveEndpoint(h.ctx(t), &webhookspb.SaveEndpointRequest{
			Endpoint:    input,
			SigningKeys: testKeys(),
		})
		must.Error(t, err)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.ErrorIs(t, err, webhooks.ErrUnknownEventType)
	})

	T.Run("refuses an endpoint subscribing to nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		input := endpointInput()
		input.EventTypes = nil

		_, err := h.server.SaveEndpoint(h.ctx(t), &webhookspb.SaveEndpointRequest{
			Endpoint:    input,
			SigningKeys: testKeys(),
		})
		must.Error(t, err)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.ErrorIs(t, err, webhooks.ErrNoEvents)
	})

	// The URL check is the reason the write goes through the dispatcher at all:
	// an endpoint is a user-supplied URL this deployment then makes authenticated
	// requests to.
	T.Run("refuses a URL the checker rejects", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, webhooks.WithDispatcherURLChecker(
			func(context.Context, string) error { return webhooks.ErrDisallowedEndpointHost },
		))

		_, err := h.server.SaveEndpoint(h.ctx(t), &webhookspb.SaveEndpointRequest{
			Endpoint:    endpointInput(),
			SigningKeys: testKeys(),
		})
		must.Error(t, err)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.ErrorIs(t, err, webhooks.ErrDisallowedEndpointHost)
	})

	// An endpoint does not change hands. The identifier is the caller's to
	// choose, so the collision is theirs to hear about — and what they are told
	// is that it is not available, never that somebody else is holding it.
	T.Run("refuses an identifier another tenant registered", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		seeded := h.seed(t, otherScope)

		input := endpointInput()
		input.Id = seeded.ID

		_, err := h.server.SaveEndpoint(h.ctx(t), &webhookspb.SaveEndpointRequest{
			Endpoint:    input,
			SigningKeys: testKeys(),
		})
		must.Error(t, err)

		test.EqOp(t, codes.AlreadyExists, status.Code(err))
		test.ErrorIs(t, err, webhooks.ErrEndpointOutOfScope)
		test.StrNotContains(t, status.Convert(err).Message(), "another scope")
	})

	// The re-registration case: an endpoint the caller already has keeps its
	// identity, and the subscription set is reconciled rather than replaced.
	T.Run("reconciles the subscriptions of an endpoint it already has", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		seeded := h.seed(t, testScope, orderCreated, orderShipped)

		input := endpointInput()
		input.Id = seeded.ID
		input.EventTypes = []string{string(orderShipped)}

		res, err := h.server.SaveEndpoint(h.ctx(t), &webhookspb.SaveEndpointRequest{
			Endpoint:    input,
			SigningKeys: testKeys(),
		})
		must.NoError(t, err)

		test.EqOp(t, seeded.ID, res.GetResult().GetId())
		must.SliceLen(t, 1, res.GetResult().GetSubscriptions())
		test.EqOp(t, string(orderShipped), res.GetResult().GetSubscriptions()[0].GetEventType())
	})

	T.Run("refuses a caller with no principal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.SaveEndpoint(t.Context(), &webhookspb.SaveEndpointRequest{
			Endpoint:    endpointInput(),
			SigningKeys: testKeys(),
		})
		must.Error(t, err)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestGetEndpoint(T *testing.T) {
	T.Parallel()

	T.Run("reads one of the caller's endpoints", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope)

		res, err := h.server.GetEndpoint(h.ctx(t), &webhookspb.GetEndpointRequest{EndpointId: seeded.ID})
		must.NoError(t, err)
		must.NotNil(t, res.GetResult())

		test.EqOp(t, seeded.ID, res.GetResult().GetId())
	})

	// The scope is bound into the statement rather than checked in front of it,
	// so another tenant's endpoint is not there rather than forbidden.
	T.Run("cannot read another tenant's endpoint", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope)

		_, err := h.server.GetEndpoint(h.otherCtx(t), &webhookspb.GetEndpointRequest{EndpointId: seeded.ID})
		must.Error(t, err)

		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("refuses a caller with no principal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.GetEndpoint(t.Context(), &webhookspb.GetEndpointRequest{EndpointId: "whatever"})
		must.Error(t, err)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestListEndpoints(T *testing.T) {
	T.Parallel()

	T.Run("pages only the caller's endpoints", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		mine := h.seed(t, testScope)
		h.seed(t, otherScope)

		res, err := h.server.ListEndpoints(h.ctx(t), &webhookspb.ListEndpointsRequest{})
		must.NoError(t, err)

		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, mine.ID, res.GetResults()[0].GetId())
		test.NotNil(t, res.GetPagination())
	})

	T.Run("refuses a caller with no principal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.ListEndpoints(t.Context(), &webhookspb.ListEndpointsRequest{})
		must.Error(t, err)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestArchiveEndpoint(T *testing.T) {
	T.Parallel()

	T.Run("retires one of the caller's endpoints", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		ctx := h.ctx(t)

		seeded := h.seed(t, testScope)

		_, err := h.server.ArchiveEndpoint(ctx, &webhookspb.ArchiveEndpointRequest{EndpointId: seeded.ID})
		must.NoError(t, err)

		read, err := h.server.GetEndpoint(ctx, &webhookspb.GetEndpointRequest{EndpointId: seeded.ID})
		must.NoError(t, err)

		// The row is kept rather than deleted: the attempts log outlives the
		// endpoint, because "what did we send them" is asked most often after
		// somebody has been removed.
		test.NotNil(t, read.GetResult().GetArchivedAt())
	})

	// Another tenant's archive is a no-op that answers OK, and both halves of
	// that are deliberate.
	//
	// The statement binds the caller's scope, so it matches nothing and the row
	// is untouched — which is the half that matters. The OK is
	// webhooks.Store.ArchiveEndpoint's own decision to drop the affected-row
	// count: an archive that named nothing and an archive of something already
	// archived are both "this endpoint is not live", which is the state the
	// caller asked for. Answering NotFound instead would make this RPC say
	// whether an identifier exists in somebody else's tenant, which is precisely
	// what the reads refuse to say.
	T.Run("another tenant's archive touches nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope)

		_, err := h.server.ArchiveEndpoint(h.otherCtx(t), &webhookspb.ArchiveEndpointRequest{EndpointId: seeded.ID})
		must.NoError(t, err)

		read, err := h.store.GetEndpoint(h.ctx(t), h.db.Reader(), testScope, seeded.ID)
		must.NoError(t, err)
		test.Nil(t, read.ArchivedAt)
	})

	// And the same answer for an identifier that names nothing anywhere, which
	// is what makes the previous case disclose nothing: a caller cannot tell the
	// two apart.
	T.Run("an unknown identifier archives nothing and says so the same way", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.ArchiveEndpoint(h.ctx(t), &webhookspb.ArchiveEndpointRequest{
			EndpointId: "no_such_endpoint",
		})
		test.NoError(t, err)
	})

	T.Run("refuses a caller with no principal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.ArchiveEndpoint(t.Context(), &webhookspb.ArchiveEndpointRequest{EndpointId: "whatever"})
		must.Error(t, err)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}
