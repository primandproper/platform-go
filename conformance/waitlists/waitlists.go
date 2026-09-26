package waitlists

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "waitlists"

// Suite is the signup surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
		Mounted: func(s conformance.Surfaces) bool { return s.Waitlists != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("lists", func(t *testing.T) {
		t.Parallel()
		lists(t, s)
	})
	t.Run("signups", func(t *testing.T) {
		t.Parallel()
		signups(t, s)
	})
	t.Run("the signup page", func(t *testing.T) {
		t.Parallel()
		signupPage(t, s)
	})
	t.Run("erasure", func(t *testing.T) {
		t.Parallel()
		erasure(t, s)
	})
}
