package webhooks

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// eventTypes is the read a subscription form makes before it can render
// anything.
func eventTypes(t *testing.T, s *conformance.Session) {
	t.Helper()

	// The list is what SaveEndpoint judges a subscription against, which is the
	// whole reason the read exists: a form rendered from it can only offer what
	// a write will accept. One endpoint subscribing to all of them is the same
	// claim as one per type, made in one write.
	t.Run("every event type offered is one an endpoint may subscribe to", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		offered := catalog(t, caller, 1)

		saved := registered(t, caller, offered...)

		got := subscribedEventTypes(saved)
		for _, eventType := range offered {
			test.SliceContains(t, got, eventType,
				test.Sprintf("the catalog offered %q and the endpoint came back without it", eventType))
		}
	})

	t.Run("the catalog is offered in a stable order", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		offered := catalog(t, caller, 1)

		for i := 1; i < len(offered); i++ {
			test.True(t, offered[i] > offered[i-1],
				test.Sprintf("%q is offered after %q", offered[i], offered[i-1]))
		}

		// And the same order twice, which is what "stable" means to a form
		// that re-renders.
		must.Eq(t, offered, catalog(t, caller, 1))
	})
}
