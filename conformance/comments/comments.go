package comments

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "comments"

// Suite is the discussion surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
		Mounted: func(s conformance.Surfaces) bool { return s.Comments != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("writing", func(t *testing.T) {
		t.Parallel()
		writing(t, s)
	})
	t.Run("reading", func(t *testing.T) {
		t.Parallel()
		reading(t, s)
	})
	t.Run("authorship", func(t *testing.T) {
		t.Parallel()
		authorship(t, s)
	})
	t.Run("confinement", func(t *testing.T) {
		t.Parallel()
		confinement(t, s)
	})
}
