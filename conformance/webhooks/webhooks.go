package webhooks

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "webhooks"

// The calls this suite makes, as the names a caller is minted to make them by.
// A caller that registers an endpoint makes archiveEndpoint as well, since
// register retires what it registered when the test ends.
const (
	addSubscription     = webhookspb.WebhooksService_AddSubscription_FullMethodName
	archiveEndpoint     = webhookspb.WebhooksService_ArchiveEndpoint_FullMethodName
	archiveSubscription = webhookspb.WebhooksService_ArchiveSubscription_FullMethodName
	getEndpoint         = webhookspb.WebhooksService_GetEndpoint_FullMethodName
	getSubscription     = webhookspb.WebhooksService_GetSubscription_FullMethodName
	listEndpoints       = webhookspb.WebhooksService_ListEndpoints_FullMethodName
	listEventTypes      = webhookspb.WebhooksService_ListEventTypes_FullMethodName
	listSubscriptions   = webhookspb.WebhooksService_ListSubscriptions_FullMethodName
	rotateSecret        = webhookspb.WebhooksService_RotateSecret_FullMethodName
	saveEndpoint        = webhookspb.WebhooksService_SaveEndpoint_FullMethodName
)

// Suite is the outbound webhook surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
		Mounted: func(s conformance.Surfaces) bool { return s.Webhooks != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("event types", func(t *testing.T) {
		t.Parallel()
		eventTypes(t, s)
	})
	t.Run("endpoints", func(t *testing.T) {
		t.Parallel()
		endpoints(t, s)
	})
	t.Run("signing keys", func(t *testing.T) {
		t.Parallel()
		signingKeys(t, s)
	})
	t.Run("subscriptions", func(t *testing.T) {
		t.Parallel()
		subscriptions(t, s)
	})
	t.Run("confinement", func(t *testing.T) {
		t.Parallel()
		confinement(t, s)
	})
}
