package webhooks

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "webhooks"

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
