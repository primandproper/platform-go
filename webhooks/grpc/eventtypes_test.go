package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v15/authentication/passkeys"
	"github.com/primandproper/platform-go/v15/authentication/passwordreset"
	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/identity"
	"github.com/primandproper/platform-go/v15/webhooks"
	webhooksgrpc "github.com/primandproper/platform-go/v15/webhooks/grpc"
	"github.com/primandproper/platform-go/v15/webhooks/webhookspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestListEventTypes is the read a subscription form makes before it can render
// anything, and the one this surface refused writes against without serving.
func TestListEventTypes(T *testing.T) {
	T.Parallel()

	T.Run("answers with the catalog the dispatcher was built with", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.ListEventTypes(h.ctx(t), &webhookspb.ListEventTypesRequest{})
		must.NoError(t, err)

		results := res.GetResults()
		must.SliceNotEmpty(t, results)

		found := false
		for _, def := range results {
			if def.GetEventType() == string(orderCreated) {
				found = true

				test.EqOp(t, "an order was placed", def.GetDescription())
			}
		}

		test.True(t, found, test.Sprintf("the catalog's own event type is absent from %v", results))
	})

	// The list is what SaveEndpoint judges against, which is the whole reason
	// this method exists. A form rendered from the answer can only offer event
	// types a write will accept.
	T.Run("every type it answers with is one a subscription may name", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.ListEventTypes(h.ctx(t), &webhookspb.ListEventTypesRequest{})
		must.NoError(t, err)

		for _, def := range res.GetResults() {
			input := endpointInput()
			input.EventTypes = []string{def.GetEventType()}

			_, saveErr := h.server.SaveEndpoint(h.ctx(t), &webhookspb.SaveEndpointRequest{
				Endpoint:    input,
				SigningKeys: testKeys(),
			})
			must.NoError(t, saveErr, must.Sprintf("the catalog offered %q and the write refused it", def.GetEventType()))
		}
	})

	// An internal event is in the catalog and refused by every write, so a list
	// offering it would be offering a value its own surface refuses.
	T.Run("leaves out the events the catalog marks internal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.ListEventTypes(h.ctx(t), &webhookspb.ListEventTypesRequest{})
		must.NoError(t, err)

		for _, def := range res.GetResults() {
			test.NotEqOp(t, string(orderAudited), def.GetEventType())
		}

		test.SliceLen(t, 2, res.GetResults())
	})

	// The harness's catalog proves the gate; this proves it holds for the
	// fragments the flag was built for, whose credential events a consumer merges
	// by habit and must never be offered.
	T.Run("leaves out the credential events platform's own fragments mark internal", func(t *testing.T) {
		t.Parallel()

		catalog, err := webhooks.Merge(
			testCatalog(),
			signin.EventCatalog(),
			passkeys.EventCatalog(),
			passwordreset.EventCatalog(),
			oauth2clients.EventCatalog(),
			identity.EventCatalog(),
		)
		must.NoError(t, err)

		h := newHarness(t, webhooks.WithCatalog(catalog))

		res, err := h.server.ListEventTypes(h.ctx(t), &webhookspb.ListEventTypesRequest{})
		must.NoError(t, err)

		offered := map[string]bool{}
		for _, def := range res.GetResults() {
			offered[def.GetEventType()] = true
		}

		internal := 0

		for eventType, definition := range catalog {
			if definition.Internal {
				internal++
			}

			test.EqOp(t, !definition.Internal, offered[string(eventType)], test.Sprintf("%s offered", eventType))
		}

		test.Positive(t, internal)
		test.SliceLen(t, len(catalog)-internal, res.GetResults())
	})

	T.Run("it is sorted, so a form renders in a stable order", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.ListEventTypes(h.ctx(t), &webhookspb.ListEventTypesRequest{})
		must.NoError(t, err)

		previous := ""
		for _, def := range res.GetResults() {
			test.True(t, def.GetEventType() > previous,
				test.Sprintf("%q follows %q", def.GetEventType(), previous))
			previous = def.GetEventType()
		}
	})

	T.Run("the method requires its own grant", func(t *testing.T) {
		t.Parallel()

		required := webhooksgrpc.Permissions()[webhookspb.WebhooksService_ListEventTypes_FullMethodName]
		test.SliceContains(t, required, webhooksgrpc.PermissionReadEventTypes)
	})
}
