package grpc_test

import (
	"testing"

	webhooksgrpc "github.com/primandproper/platform-go/v14/webhooks/grpc"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

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
